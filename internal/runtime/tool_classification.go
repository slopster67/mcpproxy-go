package runtime

import "fmt"

func (r *Runtime) GetToolClassification(server, tool string) (string, error) {
	if r.storageManager == nil {
		return "", nil
	}
	return r.storageManager.GetToolClassification(server, tool)
}
func (r *Runtime) SetToolClassificationAt(server, tool, tier, hash string, expectedIncarnation int64) error {
	if r.storageManager == nil {
		return fmt.Errorf("storage not available")
	}
	inc, err := r.CaptureApprovalIncarnation(server, expectedIncarnation)
	if err != nil {
		return err
	}
	err = r.WithServerApprovalLockAt(server, inc, func() error { return r.storageManager.SetToolClassification(server, tool, tier, hash) })
	if err != nil {
		return err
	}
	r.toolsChanged(server)
	return nil
}
