resource "aws_ecr_repository" "this" {
  for_each             = toset(var.image_repositories)
  name                 = "sentinel-${each.value}"
  image_tag_mutability = "IMMUTABLE" # a tag is a release; never overwritten
  image_scanning_configuration {
    scan_on_push = true
  }
  encryption_configuration {
    encryption_type = "KMS"
    kms_key         = aws_kms_key.this.arn
  }
}

resource "aws_ecr_lifecycle_policy" "this" {
  for_each   = aws_ecr_repository.this
  repository = each.value.name
  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "keep the last 50 images"
      selection    = { tagStatus = "any", countType = "imageCountMoreThan", countNumber = 50 }
      action       = { type = "expire" }
    }]
  })
}

resource "random_password" "app" {
  for_each = toset(["db_app_password"])
  length   = 48
  special  = false
}

# Synced into the cluster's `sentinel` Secret by External Secrets Operator (values-aws.yaml).
# No shared JWT secret in production: users come from the company IdP (verified via its JWKS), and
# service tokens are ES256-signed by the gateway with keys only it holds. Terraform creates the slots;
# the key set comes from scripts/gen_signing_key.py and the OIDC client secret from the IdP, both put
# out of band (aws secretsmanager put-secret-value), so private keys never enter Terraform state.
resource "aws_secretsmanager_secret" "app" {
  name       = "${var.name}/app"
  kms_key_id = aws_kms_key.this.arn
}

resource "aws_secretsmanager_secret_version" "app" {
  secret_id = aws_secretsmanager_secret.app.id
  secret_string = jsonencode({
    dbAppPassword    = random_password.app["db_app_password"].result
    signingKeys      = "" # set out of band; the gateway refuses to start without it (requireConfigured)
    oidcClientSecret = "" # set out of band, from the IdP's client registration
    anthropicApiKey  = "" # set out of band: aws secretsmanager put-secret-value
  })
  lifecycle {
    ignore_changes = [secret_string] # rotated/edited outside Terraform after creation
  }
}
