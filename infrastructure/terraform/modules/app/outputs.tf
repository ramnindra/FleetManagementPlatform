output "db_endpoint" {
  value = "${aws_db_instance.this.address}:5432"
}

output "secrets_prefix" {
  value = local.secrets_prefix
}

output "external_secrets_role_arn" {
  value = aws_iam_role.external_secrets.arn
}
