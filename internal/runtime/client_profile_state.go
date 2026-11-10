package runtime

import "github.com/slopster67/mcpproxy-go/internal/config"

const (
	ProfileStateNone     = "none"
	ProfileStateOK       = "ok"
	ProfileStateDangling = "dangling"
)

func ProfileStateOf(pin string, cfg *config.Config) string {
	if pin == "" {
		return ProfileStateNone
	}
	if cfg != nil {
		for i := range cfg.Profiles {
			if cfg.Profiles[i].ID == pin {
				return ProfileStateOK
			}
		}
	}
	return ProfileStateDangling
}
