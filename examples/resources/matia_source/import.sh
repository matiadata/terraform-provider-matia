# Import preserves the source agent reported by the API.
# Supply connection_config and connection_secrets separately; the public API
# does not return credentials. Configure agent_id to retain the binding.
terraform import matia_source.example source-id
