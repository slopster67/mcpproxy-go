package httpapi

import "context"

type profileToolVisibilityController interface {
	SearchToolsForProfile(context.Context, string, int) ([]map[string]interface{}, bool, error)
}
