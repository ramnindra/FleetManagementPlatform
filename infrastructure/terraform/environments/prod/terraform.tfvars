environment             = "prod"
aws_region              = "us-east-1"
availability_zone_count = 3

eks_node_instance_types = ["t3.large"]
eks_node_min_size       = 3
eks_node_max_size       = 10
eks_node_desired_size   = 3
# eks_endpoint_public_access stays true (the default) here deliberately: this
# project has no bastion/VPN set up to reach a private-only API server, so
# flipping this to false would make the cluster unmanageable from outside the
# VPC with nothing else built to compensate. Real hardening is
# eks_public_access_cidrs restricted to a known office/VPN CIDR, not a blanket
# flip to private-only — see the variable's description in variables.tf.
eks_public_access_cidrs = ["203.0.113.0/24"] # placeholder — replace with your actual office/VPN egress CIDR before applying

db_instance_class        = "db.t3.medium"
db_allocated_storage_gb  = 50
db_multi_az              = true
db_backup_retention_days = 30
