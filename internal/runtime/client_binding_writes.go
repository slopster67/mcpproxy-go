package runtime

// LockBindingWrites serializes every guarded write: the guard check and the
// write it protects run under this one mutex, so two individually
// safe concurrent writes (a config write and a binding write) can never
// combine into a bypassable state. Callers must not hold it across another
// LockBindingWrites call.
func (r *Runtime) LockBindingWrites() (unlock func()) {
	r.bindingWriteMu.Lock()
	return r.bindingWriteMu.Unlock
}

// ClientsService returns the runtime's single client-credential service.
func (r *Runtime) ClientsService() *ClientsService { return r.clientsService }
