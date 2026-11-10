package upstream

// SetConnectionWake connects recovery notifications to the connection controller.
// Callbacks only report state; they never start a second dialing loop.
func (m *Manager) SetConnectionWake(wake func()) {
	m.connectionWake.Store(&wake)
}

func (m *Manager) wakeConnections() {
	if wake := m.connectionWake.Load(); wake != nil && *wake != nil {
		(*wake)()
	}
}

// InvalidateConnection asks the controller to refresh an upstream whose peer
// reports a changed connection. The controller performs the actual teardown
// and connection under the usual generation and in-flight-call checks.
func (m *Manager) InvalidateConnection(name string) {
	m.mu.RLock()
	client := m.clients[name]
	stopping := m.shuttingDown
	m.mu.RUnlock()
	if stopping || client == nil {
		return
	}
	if client.PrepareConnectionRetry(true) {
		m.wakeConnections()
	}
}
