variable "region" {
  description = "SES region. us-east-1 is where account 871376691052 has SES production access (ADR-0011, section 6); production access is per region, so moving this needs a new request first."
  type        = string
  default     = "us-east-1"
}

variable "hosted_zone" {
  type    = string
  default = "unifyai.us"
}

variable "dmarc_report_email" {
  description = <<-EOT
    Where DMARC aggregate reports (rua) go. No default: a p=none policy whose
    reports nobody reads is a record, not a control. An address outside
    unifyai.us only receives reports once its own domain authorises it with
    a TXT record "v=DMARC1" at compute.unifyai.us._report._dmarc.<its domain>
    (RFC 7489, section 7.1); without that, compliant receivers drop them.
  EOT
  type        = string

  validation {
    condition     = can(regex("^[^@\\s;,!]+@[^@\\s;,!]+\\.[^@\\s;,!]+$", var.dmarc_report_email))
    error_message = "dmarc_report_email must be a plain address; it is written into the DMARC record as mailto:<address>."
  }
}
