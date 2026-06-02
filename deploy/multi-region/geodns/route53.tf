# Sample Route53 latency-based routing for UBAG multi-region deployment.
# Replace placeholder values before applying.

variable "hosted_zone_id" {
  description = "Route53 hosted zone ID for the UBAG domain."
  type        = string
}

variable "region_a_ip" {
  description = "Public IP address of the region-a Caddy edge node."
  type        = string
}

variable "region_b_ip" {
  description = "Public IP address of the region-b Caddy edge node."
  type        = string
}

resource "aws_route53_record" "ubag_region_a" {
  zone_id        = var.hosted_zone_id
  name           = "api.ubag.example.com"
  type           = "A"
  ttl            = 30
  set_identifier = "region-a"

  latency_routing_policy {
    region = "us-west-2" # adjust to your actual region
  }

  records = [var.region_a_ip]

  health_check_id = aws_route53_health_check.region_a.id
}

resource "aws_route53_health_check" "region_a" {
  fqdn              = "gateway-a.ubag.example.com"
  port              = 443
  type              = "HTTPS"
  resource_path     = "/v1/ready"
  failure_threshold = 3
  request_interval  = 30
  tags              = { Name = "ubag-region-a-readiness" }
}

resource "aws_route53_record" "ubag_region_b" {
  zone_id        = var.hosted_zone_id
  name           = "api.ubag.example.com"
  type           = "A"
  ttl            = 30
  set_identifier = "region-b"

  latency_routing_policy {
    region = "eu-west-1"
  }

  records = [var.region_b_ip]

  health_check_id = aws_route53_health_check.region_b.id
}

resource "aws_route53_health_check" "region_b" {
  fqdn              = "gateway-b.ubag.example.com"
  port              = 443
  type              = "HTTPS"
  resource_path     = "/v1/ready"
  failure_threshold = 3
  request_interval  = 30
  tags              = { Name = "ubag-region-b-readiness" }
}
