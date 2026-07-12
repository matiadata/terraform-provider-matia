# The Matia provider reads credentials from the environment by default:
#   export MATIA_API_TOKEN="your-api-key"
#   export MATIA_API_URL="https://api.matia.io/v1"  # optional; this is the default
#
# You can also set api_token and api_url directly in the block below; explicit
# values take precedence over the environment.
provider "matia" {}
