# Public entry: one ALB, host-header rules to one target group per app
# (local.apps in instance.tf). HTTP exists only to redirect.

resource "aws_lb" "this" {
  name                       = var.name
  load_balancer_type         = "application"
  security_groups            = [aws_security_group.alb.id]
  subnets                    = aws_subnet.public[*].id
  idle_timeout               = 300 # log streaming and long console requests
  enable_deletion_protection = var.alb_deletion_protection
  drop_invalid_header_fields = true

  access_logs {
    bucket  = aws_s3_bucket.alb_logs.id
    enabled = true
  }

  # The bucket policy must exist before the load balancer tests delivery, or
  # the apply fails with "Access Denied for bucket".
  depends_on = [aws_s3_bucket_policy.alb_logs]
}

resource "aws_lb_target_group" "app" {
  for_each = local.apps

  name        = "${var.name}-${each.key}"
  port        = each.value.port
  protocol    = "HTTP"
  vpc_id      = aws_vpc.this.id
  target_type = "instance"

  health_check {
    path                = each.value.health
    matcher             = "200"
    interval            = 30
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }
}

resource "aws_lb_target_group_attachment" "app" {
  for_each = local.apps

  target_group_arn = aws_lb_target_group.app[each.key].arn
  target_id        = aws_instance.this.id
  port             = each.value.port
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

  # Set at the edge so every app behind it gets it, including the fixed 404.
  # Only on HTTPS: HSTS over plain HTTP is ignored by browsers anyway.
  routing_http_response_strict_transport_security_header_value = "max-age=${var.hsts_max_age}"

  # Anything not matching a host rule gets a fixed 404 rather than a default
  # target: a request for an unexpected Host must never land on any app.
  default_action {
    type = "fixed-response"
    fixed_response {
      content_type = "text/plain"
      status_code  = "404"
      message_body = "not found"
    }
  }
}

resource "aws_lb_listener_rule" "app" {
  for_each = local.apps

  listener_arn = aws_lb_listener.https.arn
  priority     = each.value.priority

  action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.app[each.key].arn
  }

  condition {
    host_header {
      values = [each.value.domain]
    }
  }
}
