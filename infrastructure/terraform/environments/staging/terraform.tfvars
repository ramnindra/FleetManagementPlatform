environment             = "staging"
aws_region              = "us-east-1"
availability_zone_count = 2

eks_node_instance_types = ["t3.medium"]
eks_node_min_size       = 2
eks_node_max_size       = 4
eks_node_desired_size   = 2

db_instance_class        = "db.t3.small"
db_allocated_storage_gb  = 20
db_multi_az              = false
db_backup_retention_days = 7
