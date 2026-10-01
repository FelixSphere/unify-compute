# Public entry: one ALB, one certificate covering both hostnames, host-header
# rules to two target groups. HTTP exists only to redirect.

data "aws_route53_zone" "this" {
  name         = var.hosted_zone
  private_zone = false
}

resource "aws_acm_certificate" "this" {
  domain_name               = var.compute_domain
  subject_alternative_names = [var.console_domain]
  validation_method         = "DNS"

  lifecycle {
    create_before_destroy = true
  }

  tags = { Name = var.name }
}

resource "aws_route53_record" "validation" {
  for_each = {
    for o in aws_acm_certificate.this.domain_validation_options : o.domain_name => {
      name   = o.resource_record_name
      record = o.resource_record_value
      type   = o.resource_record_type
    }
  }

  zone_id         = data.aws_route53_zone.this.zone_id
  name            = each.value.name
  type            = each.value.type
  records         = [each.value.record]
  ttl             = 60
  allow_overwrite = true
}

resource "aws_acm_certificate_validation" "this" {
  certificate_arn         = aws_acm_certificate.this.arn
  validation_record_fqdns = [for r in aws_route53_record.validation : r.fqdn]
}

resource "aws_lb" "this" {
  name               = var.name
  load_balancer_type = "application"
  security_groups    = [aws_security_group.alb.id]
  subnets            = aws_subnet.public[*].id
  idle_timeout       = 300 # log streaming and long console requests

  access_logs {
    bucket  = aws_s3_bucket.alb_logs.id
    enabled = true
  }

  # The bucket policy must exist before the load balancer tests delivery, or
  # the apply fails with "Access Denied for bucket".
  depends_on = [aws_s3_bucket_policy.alb_logs]
}

resource "aws_lb_target_group" "compute_api" {
  name        = "${var.name}-api"
  port        = 8080
  protocol    = "HTTP"
  vpc_id      = aws_vpc.this.id
  target_type = "instance"

  health_check {
    path                = "/healthz"
    matcher             = "200"
    interval            = 30
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }
}

resource "aws_lb_target_group" "console" {
  name        = "${var.name}-console"
  port        = 3000
  protocol    = "HTTP"
  vpc_id      = aws_vpc.this.id
  target_type = "instance"

  health_check {
    path                = "/api/status"
    matcher             = "200"
    interval            = 30
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }
}

resource "aws_lb_target_group_attachment" "compute_api" {
  target_group_arn = aws_lb_target_group.compute_api.arn
  target_id        = aws_instance.this.id
  port             = 8080
}

resource "aws_lb_target_group_attachment" "console" {
  target_group_arn = aws_lb_target_group.console.arn
  target_id        = aws_instance.this.id
  port             = 3000
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.this.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type = "redirect"
    redirect {
      port        = "443"
      protocol    = "HTTPS"
      status_code = "HTTP_301"
    }
  }
}

resource "aws_lb_listener" "https" {
  load_balancer_arn = aws_lb.this.arn
  port              = 443
  protocol          = "HTTPS"
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"
  certificate_arn   = aws_acm_certificate_validation.this.certificate_arn

  # Anything not matching a host rule gets a fixed 404 rather than a default
  # target: a request for an unexpected Host must never land on either app.
  default_action {
    type = "fixed-response"
    fixed_response {
      content_type = "text/plain"
      status_code  = "404"
      message_body = "not found"
    }
  }
}

resource "aws_lb_listener_rule" "compute_api" {
  listener_arn = aws_lb_listener.https.arn
  priority     = 10

  action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.compute_api.arn
  }

  condition {
    host_header {
      values = [var.compute_domain]
    }
  }
}

resource "aws_lb_listener_rule" "console" {
  listener_arn = aws_lb_listener.https.arn
  priority     = 20

  action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.console.arn
  }

  condition {
    host_header {
      values = [var.console_domain]
    }
  }
}

resource "aws_route53_record" "compute" {
  zone_id = data.aws_route53_zone.this.zone_id
  name    = var.compute_domain
  type    = "A"

  alias {
    name                   = aws_lb.this.dns_name
    zone_id                = aws_lb.this.zone_id
    evaluate_target_health = false
  }
}

resource "aws_route53_record" "console" {
  zone_id = data.aws_route53_zone.this.zone_id
  name    = var.console_domain
  type    = "A"

  alias {
    name                   = aws_lb.this.dns_name
    zone_id                = aws_lb.this.zone_id
    evaluate_target_health = false
  }
}

# ---------------------------------------------------------------------------
# Access logs.
#
# UnifyAPI production runs with ALB access logs OFF, which is the first blind
# spot its SRE agent lists: there is no per-request record at the edge, so
# questions about who called what, and how often, have no source. Container
# logs never leave that box either.
#
# Turning this on costs one bucket and is free to do before the first apply.
# Retrofitting it means a listener-adjacent change to a live load balancer.
# ---------------------------------------------------------------------------

data "aws_elb_service_account" "this" {}

resource "aws_s3_bucket" "alb_logs" {
  bucket = "${var.name}-alb-logs-${data.aws_caller_identity.current.account_id}"

  # Staging is meant to be destroyable. Logs here are diagnostic, not records
  # anyone must keep; without this a `terraform destroy` fails on a non-empty
  # bucket and leaves the stack half-torn-down.
  force_destroy = true

  tags = { Name = "${var.name}-alb-logs" }
}

resource "aws_s3_bucket_public_access_block" "alb_logs" {
  bucket                  = aws_s3_bucket.alb_logs.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "alb_logs" {
  bucket = aws_s3_bucket.alb_logs.id

  rule {
    # ALB access-log delivery supports SSE-S3 only; SSE-KMS silently drops
    # deliveries rather than failing the apply.
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "alb_logs" {
  bucket = aws_s3_bucket.alb_logs.id

  rule {
    id     = "expire"
    status = "Enabled"
    filter {}

    expiration {
      days = 30 # staging: long enough to investigate last week, not a archive
    }

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }
}

data "aws_iam_policy_document" "alb_logs" {
  # us-east-1 is an older ELB region: delivery comes from the regional ELB
  # account, not from the logdelivery service principal used by newer regions.
  statement {
    principals {
      type        = "AWS"
      identifiers = [data.aws_elb_service_account.this.arn]
    }
    actions   = ["s3:PutObject"]
    resources = ["${aws_s3_bucket.alb_logs.arn}/*"]
  }

  statement {
    principals {
      type        = "Service"
      identifiers = ["delivery.logs.amazonaws.com"]
    }
    actions   = ["s3:PutObject"]
    resources = ["${aws_s3_bucket.alb_logs.arn}/*"]
    condition {
      test     = "StringEquals"
      variable = "s3:x-amz-acl"
      values   = ["bucket-owner-full-control"]
    }
  }

  statement {
    principals {
      type        = "Service"
      identifiers = ["delivery.logs.amazonaws.com"]
    }
    actions   = ["s3:GetBucketAcl"]
    resources = [aws_s3_bucket.alb_logs.arn]
  }
}

resource "aws_s3_bucket_policy" "alb_logs" {
  bucket = aws_s3_bucket.alb_logs.id
  policy = data.aws_iam_policy_document.alb_logs.json
}
