package logs

import "go.uber.org/zap"

// ChildOutputField marks messages originating in a server subprocess.
func ChildOutputField() zap.Field { return zap.Bool("child_output", true) }
