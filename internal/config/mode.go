package config

import "fmt"

// Run modes.
//
// A mode is a bundle of defaults rather than a setting of its own. It exists so
// that "run this one in debug" is one word instead of remembering which three
// things to turn up, which is the kind of thing that gets remembered wrong at
// exactly the moment it matters.
const (
	// ModeProduction is the default. It assumes the output document is what
	// anyone actually wants and keeps the log quiet enough to read.
	ModeProduction = "production"

	// ModeDebug assumes a person or an agent is watching the run itself. The
	// log level drops to debug, which names the token behind every flag, and
	// terminal output stops truncating attribute values, because at that point
	// the whole payload is the thing being looked for.
	ModeDebug = "debug"
)

// Modes lists the accepted values, for validation and for the help text.
var Modes = []string{ModeProduction, ModeDebug}

// applyMode adjusts the defaults a mode implies.
//
// It runs before the config file, the environment and the flags, so that
// anything set explicitly still wins. A mode decides what a run does when it is
// not told otherwise; it never overrides an instruction.
func (c *Config) applyMode(mode string) {
	c.Mode = mode

	if mode != ModeDebug {
		return
	}

	c.LogLevel = "debug"

	// Zero means no limit. In debug the reason to be reading the terminal at
	// all is usually the value that would otherwise have been cut.
	c.ConsoleValueLimit = 0
}

// validateMode rejects a mode this build does not know.
//
// Unlike an unrecognised log level, which falls back to info, a wrong mode is
// worth refusing: it is asked for deliberately, and silently running in
// production mode when debug was requested would waste the run it was needed
// for.
func validateMode(mode string) error {
	for _, known := range Modes {
		if mode == known {
			return nil
		}
	}

	return fmt.Errorf("--mode must be one of %v, got %q", Modes, mode)
}
