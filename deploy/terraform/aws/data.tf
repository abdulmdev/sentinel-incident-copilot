resource "aws_kms_key" "this" {
  description         = "${var.name}: EKS secrets, RDS, MSK, ElastiCache and Secrets Manager encryption"
  enable_key_rotation = true
}

# Data stores accept traffic only from the EKS cluster security group.
resource "aws_security_group" "data" {
  name   = "${var.name}-data"
  vpc_id = aws_vpc.this.id
}

resource "aws_vpc_security_group_ingress_rule" "data" {
  for_each                     = { postgres = 5432, kafka_tls = 9094, redis = 6379 }
  security_group_id            = aws_security_group.data.id
  referenced_security_group_id = aws_eks_cluster.this.vpc_config[0].cluster_security_group_id
  from_port                    = each.value
  to_port                      = each.value
  ip_protocol                  = "tcp"
  description                  = "${each.key} from EKS"
}

# --- Postgres 16 + pgvector (RDS supports the vector extension) -----------------------------
resource "aws_db_subnet_group" "this" {
  name       = var.name
  subnet_ids = aws_subnet.private[*].id
}

resource "aws_db_parameter_group" "this" {
  name   = "${var.name}-pg16"
  family = "postgres16"
  parameter {
    name  = "rds.force_ssl"
    value = "1"
  }
}

resource "aws_db_instance" "this" {
  identifier                          = var.name
  engine                              = "postgres"
  engine_version                      = "16"
  instance_class                      = var.db_instance_class
  allocated_storage                   = 100
  max_allocated_storage               = 500
  storage_encrypted                   = true
  kms_key_id                          = aws_kms_key.this.arn
  db_name                             = "sentinel"
  username                            = "sentinel" # migrations role; services use sentinel_app (RLS-restricted)
  manage_master_user_password         = true       # rotated by RDS in Secrets Manager
  multi_az                            = true
  db_subnet_group_name                = aws_db_subnet_group.this.name
  vpc_security_group_ids              = [aws_security_group.data.id]
  parameter_group_name                = aws_db_parameter_group.this.name
  backup_retention_period             = 14
  deletion_protection                 = true
  performance_insights_enabled        = true
  copy_tags_to_snapshot               = true
  skip_final_snapshot                 = false
  final_snapshot_identifier           = "${var.name}-final"
  auto_minor_version_upgrade          = true
  iam_database_authentication_enabled = true
}

# --- Kafka (MSK), TLS in transit ----------------------------------------------------------------
resource "aws_msk_cluster" "this" {
  cluster_name           = var.name
  kafka_version          = "3.8.x"
  number_of_broker_nodes = 3
  broker_node_group_info {
    instance_type   = var.kafka_instance_type
    client_subnets  = aws_subnet.private[*].id
    security_groups = [aws_security_group.data.id]
    storage_info {
      ebs_storage_info {
        volume_size = 200
      }
    }
  }
  encryption_info {
    encryption_at_rest_kms_key_arn = aws_kms_key.this.arn
    encryption_in_transit {
      client_broker = "TLS"
      in_cluster    = true
    }
  }
}

# --- Redis (ElastiCache) for distributed rate limits ---------------------------------------------
resource "aws_elasticache_subnet_group" "this" {
  name       = var.name
  subnet_ids = aws_subnet.private[*].id
}

resource "aws_elasticache_replication_group" "this" {
  replication_group_id       = var.name
  description                = "Sentinel rate limiting"
  engine                     = "redis"
  engine_version             = "7.1"
  node_type                  = var.redis_node_type
  num_cache_clusters         = 2
  automatic_failover_enabled = true
  multi_az_enabled           = true
  subnet_group_name          = aws_elasticache_subnet_group.this.name
  security_group_ids         = [aws_security_group.data.id]
  at_rest_encryption_enabled = true
  transit_encryption_enabled = true
  kms_key_id                 = aws_kms_key.this.arn
}
