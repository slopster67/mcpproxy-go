export interface OAuthOverrides {
  mode?: 'on' | 'off'
  client_id?: string
  client_secret?: string
  scopes?: string[]
  extra_params?: Record<string, string>
  redirect_uri?: string
  authorization_endpoint?: string
  token_endpoint?: string
  registration_endpoint?: string
  auth_server_metadata_url?: string
}

// The values OAuth discovery resolves for a server.
export interface OAuthDiscovered {
  automatic: OAuthOverrides
  discovery_error?: string
}
