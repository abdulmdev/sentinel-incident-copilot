variable "region" {
  type    = string
  default = "eu-west-1"
}

variable "environment" {
  type    = string
  default = "prod"
}

variable "name" {
  type    = string
  default = "sentinel"
}

variable "vpc_cidr" {
  type    = string
  default = "10.40.0.0/16"
}

variable "kubernetes_version" {
  type    = string
  default = "1.33"
}

variable "node_instance_types" {
  type    = list(string)
  default = ["m7g.large"] # Graviton: the images are built for arm64 and amd64
}

variable "node_count" {
  type    = object({ min = number, desired = number, max = number })
  default = { min = 2, desired = 3, max = 8 }
}

variable "db_instance_class" {
  type    = string
  default = "db.r7g.large"
}

variable "kafka_instance_type" {
  type    = string
  default = "kafka.m7g.large"
}

variable "redis_node_type" {
  type    = string
  default = "cache.r7g.large"
}

variable "image_repositories" {
  type    = list(string)
  default = ["gateway", "ingestion", "agent", "mcp-observability", "web", "chaos-lab"]
}
