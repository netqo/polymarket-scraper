package config

// LogCategories switches classes of log record on and off.
//
// Level is the wrong axis for the question this answers. Something reading a
// run wants every per-token flag and none of the keepalive chatter, or the
// reverse while chasing a disconnect, and both of those sit at the same level:
// raising it to quieten one silences the other with it.
//
// It is a struct of booleans rather than a map because Config is compared with
// ==, both in its own tests and wherever a caller wants to know whether two
// runs were configured the same way. A map would make Config uncomparable and
// that comparison would stop compiling.
//
// Errors are never affected by any of these. A category says what a record is
// about, not whether it matters, and something has to reach a reader who has
// switched everything else off, or a silent run and a broken one look alike.
type LogCategories struct {
	// Startup is the configuration record and what was odd about the inputs.
	Startup bool

	// Progress is the run reaching its stages.
	Progress bool

	// Connection is sockets opening, closing and misbehaving.
	Connection bool

	// Flags is per-token observations as the trackers raise them. The highest
	// volume of the lot on a bad run, and the most useful.
	Flags bool

	// REST is fetches that did not go to plan.
	REST bool

	// Decode is a message the scraper could not read.
	Decode bool

	// Discovery is tokens taken on mid-window from announcements.
	Discovery bool
}

// AllLogCategories returns every category switched on, which is the default.
//
// Off by default would be the wrong way round: a run that quietly says nothing
// about a whole class of problem is worse than a noisy one, and someone who
// wants less can say so.
func AllLogCategories() LogCategories {
	return LogCategories{
		Startup:    true,
		Progress:   true,
		Connection: true,
		Flags:      true,
		REST:       true,
		Decode:     true,
		Discovery:  true,
	}
}

// Disabled lists the categories that are switched off, by name.
//
// Names rather than the struct, so that the logging package needs no knowledge
// of this one and this one needs no knowledge of it. The command wires the two
// together, which is where that belongs.
func (c LogCategories) Disabled() []string {
	switches := []struct {
		name string
		on   bool
	}{
		{"startup", c.Startup},
		{"progress", c.Progress},
		{"connection", c.Connection},
		{"flags", c.Flags},
		{"rest", c.REST},
		{"decode", c.Decode},
		{"discovery", c.Discovery},
	}

	var off []string
	for _, s := range switches {
		if !s.on {
			off = append(off, s.name)
		}
	}

	return off
}
