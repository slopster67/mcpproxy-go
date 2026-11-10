package config

// Defaults for the tri-state numeric settings.
//
// They live here, next to the readers that apply them, rather than only in
// DefaultConfig(): two REST write paths decode a client document into a
// ZERO-valued Config — handlePatchConfig's `var merged config.Config` and
// oauth.UnmaskLiveConfigDocument's `&config.Config{}` — and never see the
// constructor. A default that lives only in DefaultConfig() is wrong on those
// paths, so the reader is the single resolution point.
const (
	defaultMaxResultSizeChars   = 500000 // Claude Code's inline-response hard max
	defaultActivityMaxSizeMB    = 256    // total activity-log size cap in MB
	defaultActivityRetentionDay = 90     // days of activity history retained
	defaultActivityMaxRecords   = 100000 // activity records retained
)

// EffectiveMaxResultSizeChars resolves the `_meta.anthropic/maxResultSizeChars`
// ceiling advertised on every tool.
//
// nil (key absent) = the built-in default. An explicit 0 = the operator
// disabled the annotation, and stays 0.
func (c *Config) EffectiveMaxResultSizeChars() int {
	if c == nil || c.MaxResultSizeChars == nil {
		return defaultMaxResultSizeChars
	}
	return *c.MaxResultSizeChars
}

// EffectiveActivityMaxSizeMB resolves the total activity-log size cap in MB.
//
// nil (key absent) = the built-in default. An explicit 0 = the size cap is
// disabled, and stays disabled.
func (c *Config) EffectiveActivityMaxSizeMB() int {
	if c == nil || c.ActivityMaxSizeMB == nil {
		return defaultActivityMaxSizeMB
	}
	return *c.ActivityMaxSizeMB
}

// EffectiveActivityRetentionDays and EffectiveActivityMaxRecords resolve the
// two remaining retention counts.
//
// These stay plain ints rather than tri-state pointers because 0 has no
// documented meaning for them: 0 and absent both resolve to the built-in
// default here, so every reader agrees.
func (c *Config) EffectiveActivityRetentionDays() int {
	if c == nil || c.ActivityRetentionDays <= 0 {
		return defaultActivityRetentionDay
	}
	return c.ActivityRetentionDays
}

func (c *Config) EffectiveActivityMaxRecords() int {
	if c == nil || c.ActivityMaxRecords <= 0 {
		return defaultActivityMaxRecords
	}
	return c.ActivityMaxRecords
}
