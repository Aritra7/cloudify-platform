variable "project_id" {
  type = string
}

variable "region" {
  type = string
}

variable "service_name" {
  type = string
}

variable "image" {
  type = string
}

variable "service_account_email" {
  type = string
}

variable "cpu" {
  type = string
}

variable "memory" {
  type = string
}

variable "min_instances" {
  type = number
}

variable "max_instances" {
  type = number
}

variable "allow_unauthenticated" {
  type = bool
}

variable "non_sensitive_environment" {
  type = map(string)
}

variable "secrets" {
  type = list(object({
    environment_variable = string
    secret               = string
    version              = string
  }))
}
