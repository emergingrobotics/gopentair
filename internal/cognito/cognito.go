// Package cognito is a minimal JSON client for the handful of AWS Cognito
// User Pool and Identity Pool calls the Pentair app uses. It avoids a
// dependency on the AWS SDK.
package cognito

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Error is a Cognito service error (HTTP 4xx/5xx with a __type).
type Error struct {
	Status  int
	Type    string // e.g. NotAuthorizedException
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("cognito %s (%d): %s", e.Type, e.Status, e.Message)
}

// Client talks to the Cognito IdP and Identity endpoints of one region.
type Client struct {
	HTTP        *http.Client
	IdPURL      string // https://cognito-idp.<region>.amazonaws.com/
	IdentityURL string // https://cognito-identity.<region>.amazonaws.com/
	UserAgent   string
}

// New returns a client for the given AWS region.
func New(region string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{
		HTTP:        hc,
		IdPURL:      "https://cognito-idp." + region + ".amazonaws.com/",
		IdentityURL: "https://cognito-identity." + region + ".amazonaws.com/",
	}
}

func (c *Client) call(ctx context.Context, url, target string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", target)
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Type     string `json:"__type"`
			Message  string `json:"message"`
			Message2 string `json:"Message"`
		}
		_ = json.Unmarshal(raw, &e)
		t := e.Type
		if i := strings.LastIndexByte(t, '#'); i >= 0 {
			t = t[i+1:]
		}
		if t == "" {
			t = "UnknownError"
		}
		msg := e.Message
		if msg == "" {
			msg = e.Message2
		}
		return &Error{Status: resp.StatusCode, Type: t, Message: msg}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("cognito %s: decode: %w", target, err)
	}
	return nil
}

// AuthResult is Cognito's AuthenticationResult.
type AuthResult struct {
	AccessToken  string `json:"AccessToken"`
	IdToken      string `json:"IdToken"`
	RefreshToken string `json:"RefreshToken,omitempty"`
	ExpiresIn    int    `json:"ExpiresIn"`
	TokenType    string `json:"TokenType"`
}

// InitiateAuthOutput is the response to InitiateAuth / RespondToAuthChallenge.
type InitiateAuthOutput struct {
	AuthenticationResult *AuthResult       `json:"AuthenticationResult,omitempty"`
	ChallengeName        string            `json:"ChallengeName,omitempty"`
	ChallengeParameters  map[string]string `json:"ChallengeParameters,omitempty"`
	Session              string            `json:"Session,omitempty"`
}

// InitiateAuth calls AWSCognitoIdentityProviderService.InitiateAuth.
func (c *Client) InitiateAuth(ctx context.Context, clientID, flow string, params map[string]string) (*InitiateAuthOutput, error) {
	var out InitiateAuthOutput
	err := c.call(ctx, c.IdPURL, "AWSCognitoIdentityProviderService.InitiateAuth", map[string]any{
		"AuthFlow":       flow,
		"ClientId":       clientID,
		"AuthParameters": params,
	}, &out)
	return &out, err
}

// RespondToAuthChallenge calls AWSCognitoIdentityProviderService.RespondToAuthChallenge.
func (c *Client) RespondToAuthChallenge(ctx context.Context, clientID, challenge, session string, responses map[string]string) (*InitiateAuthOutput, error) {
	in := map[string]any{
		"ChallengeName":      challenge,
		"ClientId":           clientID,
		"ChallengeResponses": responses,
	}
	if session != "" {
		in["Session"] = session
	}
	var out InitiateAuthOutput
	err := c.call(ctx, c.IdPURL, "AWSCognitoIdentityProviderService.RespondToAuthChallenge", in, &out)
	return &out, err
}

// GlobalSignOut invalidates all refresh tokens for the user.
func (c *Client) GlobalSignOut(ctx context.Context, accessToken string) error {
	return c.call(ctx, c.IdPURL, "AWSCognitoIdentityProviderService.GlobalSignOut",
		map[string]string{"AccessToken": accessToken}, nil)
}

// GetID calls AWSCognitoIdentityService.GetId.
func (c *Client) GetID(ctx context.Context, identityPoolID string, logins map[string]string) (string, error) {
	var out struct {
		IdentityID string `json:"IdentityId"`
	}
	err := c.call(ctx, c.IdentityURL, "AWSCognitoIdentityService.GetId",
		map[string]any{"IdentityPoolId": identityPoolID, "Logins": logins}, &out)
	return out.IdentityID, err
}

// AWSCredentials are the temporary credentials from GetCredentialsForIdentity.
type AWSCredentials struct {
	AccessKeyID  string
	SecretKey    string
	SessionToken string
	Expiration   time.Time
}

// GetCredentialsForIdentity calls AWSCognitoIdentityService.GetCredentialsForIdentity.
func (c *Client) GetCredentialsForIdentity(ctx context.Context, identityID string, logins map[string]string) (*AWSCredentials, error) {
	var out struct {
		Credentials struct {
			AccessKeyID  string  `json:"AccessKeyId"`
			SecretKey    string  `json:"SecretKey"`
			SessionToken string  `json:"SessionToken"`
			Expiration   float64 `json:"Expiration"`
		} `json:"Credentials"`
	}
	if err := c.call(ctx, c.IdentityURL, "AWSCognitoIdentityService.GetCredentialsForIdentity",
		map[string]any{"IdentityId": identityID, "Logins": logins}, &out); err != nil {
		return nil, err
	}
	cr := out.Credentials
	sec := int64(cr.Expiration)
	nsec := int64((cr.Expiration - float64(sec)) * 1e9)
	return &AWSCredentials{
		AccessKeyID:  cr.AccessKeyID,
		SecretKey:    cr.SecretKey,
		SessionToken: cr.SessionToken,
		Expiration:   time.Unix(sec, nsec),
	}, nil
}
