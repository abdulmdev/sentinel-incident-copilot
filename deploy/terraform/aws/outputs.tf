# Feed these into deploy/helm/sentinel/values-aws.yaml.
output "cluster_name" {
  value = aws_eks_cluster.this.name
}

output "rds_endpoint" {
  value = aws_db_instance.this.address
}

output "rds_master_secret_arn" {
  value = aws_db_instance.this.master_user_secret[0].secret_arn
}

output "msk_bootstrap_brokers_tls" {
  value = aws_msk_cluster.this.bootstrap_brokers_tls
}

output "redis_endpoint" {
  value = aws_elasticache_replication_group.this.primary_endpoint_address
}

output "ecr_repositories" {
  value = { for k, r in aws_ecr_repository.this : k => r.repository_url }
}

output "app_secret_arn" {
  value = aws_secretsmanager_secret.app.arn
}
