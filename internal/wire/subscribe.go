package wire

// Channel names the market data channel. It is the only channel this scraper
// ever opens: it carries public book data and requires no authentication, which
// is what keeps the tool credential-free.
const Channel = "market"

// Subscription operations for changing a live subscription.
const (
	OperationSubscribe   = "subscribe"
	OperationUnsubscribe = "unsubscribe"
)

// Subscription is the message that opens a subscription.
//
// CustomFeatureEnabled is not optional in practice: without it the connection
// never delivers the best quote, new market and market resolved events, three
// of which the output document is required to report.
type Subscription struct {
	AssetIDs             []string `json:"assets_ids"`
	Type                 string   `json:"type"`
	CustomFeatureEnabled bool     `json:"custom_feature_enabled"`
}

// NewSubscription builds the subscription message for a set of tokens.
func NewSubscription(assetIDs []string) Subscription {
	return Subscription{
		AssetIDs:             assetIDs,
		Type:                 Channel,
		CustomFeatureEnabled: true,
	}
}

// SubscriptionUpdate adds or removes tokens on a live connection.
//
// It deliberately carries no type field: the update message is a different
// shape from the opening subscription, not a repeat of it.
type SubscriptionUpdate struct {
	Operation string   `json:"operation"`
	AssetIDs  []string `json:"assets_ids"`
}

// Subscribe builds an update that adds tokens to a live connection.
func Subscribe(assetIDs []string) SubscriptionUpdate {
	return SubscriptionUpdate{Operation: OperationSubscribe, AssetIDs: assetIDs}
}

// Unsubscribe builds an update that removes tokens from a live connection.
func Unsubscribe(assetIDs []string) SubscriptionUpdate {
	return SubscriptionUpdate{Operation: OperationUnsubscribe, AssetIDs: assetIDs}
}
