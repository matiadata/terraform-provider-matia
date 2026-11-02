# source_settings is not read back on import and the API rejects most updates to it,
# so add `lifecycle { ignore_changes = [source_settings] }` to the imported resource.
import {
  to = matia_integration.example
  id = "6a4f30575d7e8a0ea5bbb790"
}
