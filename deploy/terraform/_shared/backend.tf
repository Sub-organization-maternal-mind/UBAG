# Remote State Backend Configuration
# ---------------------------------------------------------------------------
# Uncomment ONE backend block below based on your cloud provider, then run:
#   terraform init -reconfigure
#
# Important:
#   - Store all credentials outside this file (environment variables, OIDC,
#     instance roles, workload identity, etc.).  Never commit access_key or
#     secret_key values to source control.
#   - The DynamoDB table (AWS) or equivalent must be created before `init`.
#   - After switching backends, migrate existing state with:
#       terraform state pull > backup.tfstate   # backup first
#       terraform init -migrate-state
# ---------------------------------------------------------------------------

# --- AWS S3 Backend ---
# terraform {
#   backend "s3" {
#     bucket         = "ubag-terraform-state"
#     key            = "ubag/terraform.tfstate"
#     region         = "us-east-1"
#     encrypt        = true
#     dynamodb_table = "ubag-terraform-locks"
#   }
# }

# --- GCP GCS Backend ---
# terraform {
#   backend "gcs" {
#     bucket = "ubag-terraform-state"
#     prefix = "ubag"
#   }
# }

# --- Azure Blob Backend ---
# terraform {
#   backend "azurerm" {
#     resource_group_name  = "ubag-terraform-state-rg"
#     storage_account_name = "ubagterraformstate"
#     container_name       = "tfstate"
#     key                  = "ubag/terraform.tfstate"
#   }
# }
