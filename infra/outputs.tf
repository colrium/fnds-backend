output "ecr_repository_url" {
  value = aws_ecr_repository.this.repository_url
}

output "service_name" {
  value = module.service.service_name
}
