// Package restclient fetches order books from the CLOB REST endpoints.
//
// REST is not a fallback here, it is part of every run. The websocket never
// sends minimum order size or the negative risk flag, and the output document
// requires both, so a book has to be fetched over REST regardless of how well
// the websocket is working. It is also how a token recovers trust after a gap,
// which is the path that matters most: if it fails, the token is reported as a
// failure rather than having its stale book passed off as current.
//
// Everything here is paced by one shared limiter and bounded by a small number
// of attempts. Being a polite client is not the main reason: a run has a hard
// deadline, and an unbounded retry is indistinguishable from a hang.
package restclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/netqo/polymarket-scraper/internal/wire"
)

// Endpoint paths.
const (
	bookPath  = "/book"
	booksPath = "/books"
)

// maxBackoffSteps is how far the exponential shift is allowed to run.
//
// Four doublings already exceed any sensible ceiling, so nothing is lost by
// stopping there, and the shift cannot overflow however many attempts are
// configured. It is not a setting: it is a property of the arithmetic rather
// than a choice about behaviour.
const maxBackoffSteps = 4

// Fallbacks used when a caller leaves an option at zero.
//
// These are a guard against an unusable client, not a statement of policy. The
// defaults a run actually uses live in the config package, which is the one
// place they can be changed without rebuilding.
const (
	fallbackAttempts       = 3
	fallbackTimeout        = 10 * time.Second
	fallbackInitialBackoff = 250 * time.Millisecond
	fallbackMaxBackoff     = 4 * time.Second
)

// ErrNotFound reports that the exchange does not recognise a token id.
//
// It is deliberately distinct from every other failure: a token the exchange
// has never heard of is a fact about the token, and no amount of retrying will
// change it, whereas any other error might succeed on the next attempt.
var ErrNotFound = errors.New("the exchange does not recognise this token")

// Options configure a Client.
type Options struct {
	// BaseURL is the CLOB REST base, without a path.
	BaseURL string

	// Rate is the ceiling on requests per second across everything this client
	// does, retries included.
	Rate float64

	// Timeout bounds one attempt, whether or not HTTPClient is supplied.
	Timeout time.Duration

	// Attempts is how many times a request is tried.
	Attempts int

	// InitialBackoff and MaxBackoff bound the wait between attempts.
	InitialBackoff time.Duration
	MaxBackoff     time.Duration

	// MaxRetryAfter caps how long a server-supplied Retry-After is honoured. A
	// cooperative client should wait, but not past the point where waiting
	// costs more than the data is worth. Zero disables the header entirely.
	MaxRetryAfter time.Duration

	// HTTPClient replaces the default transport. Tests use it to reach an
	// in-process server; production leaves it nil.
	HTTPClient *http.Client
}

// Client talks to the CLOB REST endpoints.
type Client struct {
	base     *url.URL
	http     *http.Client
	limiter  *rate.Limiter
	attempts int
	timeout  time.Duration

	initialBackoff time.Duration
	maxBackoff     time.Duration
	maxRetryAfter  time.Duration

	requests atomic.Int64
}

// New builds a client, validating the endpoint up front so a typo is a
// configuration error rather than a failure halfway through a run.
func New(opts Options) (*Client, error) {
	base, err := url.Parse(opts.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing the REST base URL: %w", err)
	}
	if base.Host == "" {
		return nil, fmt.Errorf("the REST base URL %q has no host", opts.BaseURL)
	}
	if opts.Rate <= 0 {
		return nil, fmt.Errorf("the REST rate must be positive, got %v", opts.Rate)
	}

	timeout := orFallback(opts.Timeout, fallbackTimeout)
	initialBackoff := orFallback(opts.InitialBackoff, fallbackInitialBackoff)
	maxBackoff := orFallback(opts.MaxBackoff, fallbackMaxBackoff)

	attempts := opts.Attempts
	if attempts <= 0 {
		attempts = fallbackAttempts
	}

	// A ceiling below the floor would silently shorten the first wait rather
	// than lengthening the later ones, which is the opposite of a backoff.
	if maxBackoff < initialBackoff {
		maxBackoff = initialBackoff
	}

	// The timeout is enforced per attempt below rather than only here, because a
	// caller that supplies its own transport would otherwise silently lose the
	// bound: an option that is quietly ignored is worse than one that does not
	// exist.
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}

	return &Client{
		base: base,
		http: httpClient,
		// A burst of one paces requests smoothly rather than letting a second's
		// worth go out at once and then stalling, which reads to the far end as
		// a spike no matter what the average says.
		limiter:  rate.NewLimiter(rate.Limit(opts.Rate), 1),
		attempts: attempts,
		timeout:  timeout,

		initialBackoff: initialBackoff,
		maxBackoff:     maxBackoff,
		maxRetryAfter:  opts.MaxRetryAfter,
	}, nil
}

// orFallback returns value when it is usable, and the fallback otherwise.
func orFallback(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}

	return value
}

// Requests reports how many HTTP requests have been made, retries included.
func (c *Client) Requests() int { return int(c.requests.Load()) }

// Book fetches one token's order book.
func (c *Client) Book(ctx context.Context, tokenID string) (wire.RESTBook, error) {
	endpoint := c.base.JoinPath(bookPath)
	endpoint.RawQuery = url.Values{"token_id": []string{tokenID}}.Encode()

	var fetched wire.RESTBook
	if err := c.do(ctx, http.MethodGet, endpoint.String(), nil, &fetched); err != nil {
		return wire.RESTBook{}, fmt.Errorf("fetching the book for %s: %w", tokenID, err)
	}

	return fetched, nil
}

// Books fetches many order books in one request.
//
// This is what makes a whole-shortlist REST pass cheap: several hundred tokens
// in a single call rather than several hundred calls. The response may omit
// tokens the exchange does not recognise, so the caller must not assume it gets
// one book back per id it asked for.
func (c *Client) Books(ctx context.Context, tokenIDs []string) ([]wire.RESTBook, error) {
	if len(tokenIDs) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(wire.NewBookRequests(tokenIDs))
	if err != nil {
		return nil, fmt.Errorf("encoding the batch request: %w", err)
	}

	var fetched []wire.RESTBook
	endpoint := c.base.JoinPath(booksPath).String()
	if err := c.do(ctx, http.MethodPost, endpoint, body, &fetched); err != nil {
		return nil, fmt.Errorf("fetching %d books: %w", len(tokenIDs), err)
	}

	return fetched, nil
}

// do performs a request with pacing, retries and backoff, decoding the response
// into out.
func (c *Client) do(ctx context.Context, method, endpoint string, body []byte, out any) error {
	var lastErr error

	for attempt := 1; attempt <= c.attempts; attempt++ {
		if attempt > 1 {
			if err := sleep(ctx, c.backoffFor(attempt, lastErr)); err != nil {
				return errors.Join(lastErr, err)
			}
		}

		// Pacing applies to retries too, so backoff and the rate ceiling
		// compose instead of racing each other.
		if err := c.limiter.Wait(ctx); err != nil {
			return errors.Join(lastErr, fmt.Errorf("waiting for the rate limiter: %w", err))
		}

		err := c.attempt(ctx, method, endpoint, body, out)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, ErrNotFound), isTerminalErr(err):
			// The answer will not change; retrying only wastes the budget.
			return err
		case ctx.Err() != nil:
			return errors.Join(err, ctx.Err())
		}

		lastErr = err
	}

	return fmt.Errorf("giving up after %d attempts: %w", c.attempts, lastErr)
}

// attempt performs exactly one request.
func (c *Client) attempt(ctx context.Context, method, endpoint string, body []byte, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	// #nosec G107 -- the endpoint is built from a base URL validated at
	// construction plus a fixed path and escaped query values.
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("building the request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	c.requests.Add(1)

	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("sending the request: %w", err)
	}
	// Draining before closing lets the connection be reused, which matters when
	// several hundred requests go out in a row.
	defer func() {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}()

	if err := c.checkStatus(response); err != nil {
		return err
	}

	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding the response: %w", err)
	}

	return nil
}

// checkStatus turns an HTTP status into an error, distinguishing the ones that
// are facts from the ones that are setbacks.
func (c *Client) checkStatus(response *http.Response) error {
	switch {
	case response.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case response.StatusCode >= http.StatusBadRequest:
		return &statusError{
			Code:       response.StatusCode,
			RetryAfter: c.retryAfter(response),
			Terminal:   isTerminal(response.StatusCode),
		}
	default:
		return nil
	}
}

// isTerminalErr reports whether an error carries a status that will not change.
func isTerminalErr(err error) bool {
	var status *statusError

	return errors.As(err, &status) && status.Terminal
}

// isTerminal reports whether a status will still be the answer next time.
//
// A 4xx other than the two below is the server saying the request itself is
// wrong: a malformed token id, a payload past the size limit, a path that does
// not exist. Trying it again unchanged asks the same question and gets the same
// answer, three times over, while the run's deadline runs down. Only the token
// that provoked it can change the outcome.
//
// The exceptions earn their place. 429 is explicitly an instruction to come
// back later, and 408 is the server saying it gave up waiting rather than that
// it disagreed. 5xx stays retryable throughout: a server having a bad moment is
// the case retrying exists for.
func isTerminal(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusRequestTimeout:
		return false
	default:
		return status >= http.StatusBadRequest && status < http.StatusInternalServerError
	}
}

// statusError is a non-success HTTP status, carrying any server-supplied hint
// about when to try again.
type statusError struct {
	Code       int
	RetryAfter time.Duration

	// Terminal records that this status will still be the answer next time, so
	// retrying only spends the run's deadline.
	Terminal bool
}

// Error implements error. The server's own instruction is included when it gave
// one, since that is the difference between a failure worth retrying soon and
// one worth waiting out.
func (e *statusError) Error() string {
	switch {
	case e.RetryAfter > 0:
		return fmt.Sprintf("the server returned %d and asked to wait %v", e.Code, e.RetryAfter)
	case e.Terminal:
		return fmt.Sprintf("the server returned %d, which will not change on a retry", e.Code)
	default:
		return fmt.Sprintf("the server returned %d", e.Code)
	}
}

// retryAfter reads the Retry-After header, in seconds, ignoring the HTTP-date
// form: the exchange sends seconds, and a date form would need a trusted clock
// to interpret.
func (c *Client) retryAfter(response *http.Response) time.Duration {
	if c.maxRetryAfter <= 0 {
		return 0
	}

	seconds, err := strconv.Atoi(response.Header.Get("Retry-After"))
	if err != nil || seconds <= 0 {
		return 0
	}

	wait := time.Duration(seconds) * time.Second
	if wait > c.maxRetryAfter {
		return c.maxRetryAfter
	}

	return wait
}

// backoffFor returns how long to wait before the given attempt, preferring the
// server's own instruction when it gave one.
//
// There is no jitter, deliberately. Jitter solves the problem of many clients
// synchronising on the same retry schedule; there is one process here, and its
// requests are already paced by a shared limiter, so jitter would add
// randomness to a program that otherwise has none.
func (c *Client) backoffFor(attempt int, lastErr error) time.Duration {
	var status *statusError
	if errors.As(lastErr, &status) && status.RetryAfter > 0 {
		return status.RetryAfter
	}

	// The shift is bounded rather than trusted: a large attempt count would
	// otherwise overflow it and turn the longest backoff into no backoff at all.
	steps := min(attempt-2, maxBackoffSteps)

	return min(c.initialBackoff<<steps, c.maxBackoff)
}

// sleep waits, or gives up early if the run is ending.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
