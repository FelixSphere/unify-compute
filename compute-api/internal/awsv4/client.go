package awsv4

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client makes the STS, S3 and SES calls compute-api needs, signed with the
// credentials it is given per call.
type Client struct {
	Region string
	// S3Endpoint, STSEndpoint and SESEndpoint replace the AWS endpoints
	// (tests). With S3Endpoint set, buckets are addressed path-style under it.
	S3Endpoint  string
	STSEndpoint string
	SESEndpoint string
	HTTP        *http.Client
	Now         func() time.Time
}

// Error is an error answer from AWS.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("aws: %d %s: %s", e.Status, e.Code, e.Message) }

// IsNotFound reports a missing key (NoSuchKey) or a 404 HEAD, which has no
// body to carry a code.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.Code == "NoSuchKey" || e.Status == http.StatusNotFound)
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// ObjectURL is where key lives: virtual-hosted on AWS, path-style under
// S3Endpoint.
func (c *Client) ObjectURL(bucket, key string) *url.URL {
	esc := EscapeKey(key)
	var raw string
	if c.S3Endpoint != "" {
		raw = strings.TrimRight(c.S3Endpoint, "/") + "/" + bucket + "/" + esc
	} else {
		raw = "https://" + bucket + ".s3." + c.Region + ".amazonaws.com/" + esc
	}
	u, _ := url.Parse(raw)
	return u
}

func (c *Client) do(ctx context.Context, cr Credentials, service, method string, u *url.URL, hdr http.Header, body []byte) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	for k, vs := range hdr {
		req.Header[k] = vs
	}
	if body == nil {
		req.Body, req.ContentLength = http.NoBody, 0
	}
	Signer{Region: c.Region, Service: service}.Sign(req, cr, HashHex(body), c.now())
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode/100 != 2 {
		e := &Error{Status: resp.StatusCode}
		var x struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
			Error   struct {
				Code    string `xml:"Code"`
				Message string `xml:"Message"`
			} `xml:"Error"`
		}
		if xml.Unmarshal(b, &x) == nil {
			e.Code, e.Message = x.Code, x.Message
			if e.Code == "" {
				e.Code, e.Message = x.Error.Code, x.Error.Message
			}
		} else {
			// The JSON services (SES v2) put the code in a header, as
			// "Code:namespace", and only the message in the body.
			var j struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(b, &j)
			e.Code, _, _ = strings.Cut(resp.Header.Get("X-Amzn-Errortype"), ":")
			e.Message = j.Message
		}
		return resp, b, e
	}
	return resp, b, nil
}

// AssumeRole returns a session of roleARN narrowed by the inline session
// policy. From an instance role this is role chaining, which AWS caps at one
// hour whatever the role's MaxSessionDuration; asking for more fails.
func (c *Client) AssumeRole(ctx context.Context, cr Credentials, roleARN, sessionName, policy string, d time.Duration) (Credentials, error) {
	form := url.Values{
		"Action":          {"AssumeRole"},
		"Version":         {"2011-06-15"},
		"RoleArn":         {roleARN},
		"RoleSessionName": {sessionName},
		"DurationSeconds": {strconv.Itoa(int(d / time.Second))},
	}
	if policy != "" {
		form.Set("Policy", policy)
	}
	raw := c.STSEndpoint
	if raw == "" {
		raw = "https://sts." + c.Region + ".amazonaws.com/"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Credentials{}, err
	}
	hdr := http.Header{"Content-Type": {"application/x-www-form-urlencoded; charset=utf-8"}}
	_, b, err := c.do(ctx, cr, "sts", http.MethodPost, u, hdr, []byte(form.Encode()))
	if err != nil {
		return Credentials{}, err
	}
	var r struct {
		Creds struct {
			AccessKeyId     string    `xml:"AccessKeyId"`
			SecretAccessKey string    `xml:"SecretAccessKey"`
			SessionToken    string    `xml:"SessionToken"`
			Expiration      time.Time `xml:"Expiration"`
		} `xml:"AssumeRoleResult>Credentials"`
	}
	if err := xml.Unmarshal(b, &r); err != nil {
		return Credentials{}, fmt.Errorf("sts AssumeRole response: %w", err)
	}
	if r.Creds.AccessKeyId == "" {
		return Credentials{}, errors.New("sts AssumeRole: no credentials in response")
	}
	return Credentials{AccessKeyID: r.Creds.AccessKeyId, SecretAccessKey: r.Creds.SecretAccessKey,
		SessionToken: r.Creds.SessionToken, Expires: r.Creds.Expiration}, nil
}

type Object struct {
	Key  string
	Size int64
}

// List returns every object under prefix, following continuation tokens,
// up to max objects; truncated reports that more exist.
func (c *Client) List(ctx context.Context, cr Credentials, bucket, prefix string, max int) (objs []Object, truncated bool, err error) {
	token := ""
	for {
		u := c.ObjectURL(bucket, "")
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		u.RawQuery = canonicalQuery(q)
		_, b, err := c.do(ctx, cr, "s3", http.MethodGet, u, nil, nil)
		if err != nil {
			return nil, false, err
		}
		var r struct {
			Contents []struct {
				Key  string `xml:"Key"`
				Size int64  `xml:"Size"`
			} `xml:"Contents"`
			IsTruncated           bool   `xml:"IsTruncated"`
			NextContinuationToken string `xml:"NextContinuationToken"`
		}
		if err := xml.Unmarshal(b, &r); err != nil {
			return nil, false, fmt.Errorf("s3 list: %w", err)
		}
		for _, o := range r.Contents {
			if len(objs) == max {
				return objs, true, nil
			}
			objs = append(objs, Object{Key: o.Key, Size: o.Size})
		}
		if !r.IsTruncated {
			return objs, false, nil
		}
		token = r.NextContinuationToken
	}
}

// HeadChecksum returns an object's size and the SHA-256 that S3 verified
// when it was written (base64, as S3 reports it); "" if it was written
// without one.
func (c *Client) HeadChecksum(ctx context.Context, cr Credentials, bucket, key string) (size int64, sha256b64 string, err error) {
	hdr := http.Header{"X-Amz-Checksum-Mode": {"ENABLED"}}
	resp, _, err := c.do(ctx, cr, "s3", http.MethodHead, c.ObjectURL(bucket, key), hdr, nil)
	if err != nil {
		return 0, "", err
	}
	return resp.ContentLength, resp.Header.Get("X-Amz-Checksum-Sha256"), nil
}

func (c *Client) Get(ctx context.Context, cr Credentials, bucket, key string) ([]byte, error) {
	_, b, err := c.do(ctx, cr, "s3", http.MethodGet, c.ObjectURL(bucket, key), nil, nil)
	return b, err
}

func (c *Client) Put(ctx context.Context, cr Credentials, bucket, key, contentType string, body []byte) error {
	hdr := http.Header{"Content-Type": {contentType}}
	_, _, err := c.do(ctx, cr, "s3", http.MethodPut, c.ObjectURL(bucket, key), hdr, body)
	return err
}

// Presign signs a URL for key; headers must then be sent exactly as given.
func (c *Client) Presign(cr Credentials, method, bucket, key string, headers http.Header, expires time.Duration) string {
	return Signer{Region: c.Region, Service: "s3"}.Presign(method, c.ObjectURL(bucket, key), cr, headers, expires, c.now())
}

// SendRawEmail sends one complete MIME message through SES v2 SendEmail
// and returns the message id SES assigned. from must be an identity the
// credentials may send as; configurationSet may be empty.
func (c *Client) SendRawEmail(ctx context.Context, cr Credentials, from string, to []string, raw []byte, configurationSet string) (string, error) {
	type rawContent struct {
		Data []byte `json:"Data"` // encoding/json writes []byte as base64, which is what SES expects
	}
	req := struct {
		FromEmailAddress string `json:"FromEmailAddress"`
		Destination      struct {
			ToAddresses []string `json:"ToAddresses"`
		} `json:"Destination"`
		Content struct {
			Raw rawContent `json:"Raw"`
		} `json:"Content"`
		ConfigurationSetName string `json:"ConfigurationSetName,omitempty"`
	}{FromEmailAddress: from, ConfigurationSetName: configurationSet}
	req.Destination.ToAddresses = to
	req.Content.Raw.Data = raw
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	base := c.SESEndpoint
	if base == "" {
		base = "https://email." + c.Region + ".amazonaws.com"
	}
	u, err := url.Parse(strings.TrimRight(base, "/") + "/v2/email/outbound-emails")
	if err != nil {
		return "", err
	}
	hdr := http.Header{"Content-Type": {"application/json"}}
	_, b, err := c.do(ctx, cr, "ses", http.MethodPost, u, hdr, body)
	if err != nil {
		return "", err
	}
	var r struct {
		MessageId string `json:"MessageId"`
	}
	if err := json.Unmarshal(b, &r); err != nil || r.MessageId == "" {
		return "", fmt.Errorf("ses SendEmail response: %q", b)
	}
	return r.MessageId, nil
}
