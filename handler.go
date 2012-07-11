package goauth2

import (
	"encoding/json"
	"fmt"
   "log"
	"net/http"
	"net/url"
)

// ----------------------------------------------------------------------------

// MasterHandler
// Differentiate between an OAuth request (implicit, auth codes) and an
// Access Token request
func (s *Server) MasterHandler(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	response_type := v.Get("response_type")
   var err error
	if response_type != "" {
		err = s.HandleOAuthRequest(w, r)
	} else {
      err = s.HandleAccessTokenRequest(w, r)
   }

   // Return something if there was an error
   if err != nil {
		// Encode error as json
      e := s.InterpretError(err)
	   res := make(map[string]string)
	
		res["error"] = string(e.Code())
		res["error_description"] = e.Description()
		res["error_uri"] = e.URI()

	   setQueryPairs(w.Header(),
		      "Content-Type", "application/json",
   		   "Cache-Control", "no-store",
	   	   "Pragma", "no-cache",
	      )
   	encoder := json.NewEncoder(w)
	   encoder.Encode(res)
	}
}

// HandleOAuthRequest [...]
func (s *Server) HandleOAuthRequest(w http.ResponseWriter, r *http.Request) error {
	// 1. Get all request values.
	req := s.NewOAuthRequest(r)

	// 2. Validate required parameters.
	var err error
	if req.ClientID == "" {
		// Missing ClientID: no redirect.
		err = s.NewError(ErrorCodeInvalidRequest,
			"The \"client_id\" parameter is missing.")
	} else if req.ResponseType == "" {
		err = s.NewError(ErrorCodeInvalidRequest,
			"The \"response_type\" parameter is missing.")
	} else if req.ResponseType != "code" || req.ResponseType != "token" {
		err = s.NewError(ErrorCodeUnsupportedResponseType,
			fmt.Sprintf("The response type %q is not supported.",
				req.ResponseType))
	}

	// 3. Load client and validate the redirection URI.
	var redirectURI *url.URL
	if req.ClientID != "" {
		client, clientErr := s.Store.GetClient(req.ClientID)
		if client == nil {
			// Invalid ClientID: no redirect.
			if err == nil {
				err = s.NewError(ErrorCodeInvalidRequest,
					"The \"client_id\" parameter is invalid.")
			}
		} else {
			if u, uErr := validateRedirectURI(
				client.ValidateRedirectURI(req.RedirectURI)); uErr == nil {
				redirectURI = u
			} else {
				// Missing, mismatching or invalid URI: no redirect.
				if err == nil {
					if req.RedirectURI == "" {
						err = s.NewError(ErrorCodeInvalidRequest,
							"Missing redirection URI.")
					} else {
						err = s.NewError(ErrorCodeInvalidRequest, uErr.Error())
					}
				}
			}
			if clientErr != nil && err == nil {
				// Client was not authorized.
				err = clientErr
			}
		}
	}

	// 4. If no valid redirection URI was set, abort.
	if redirectURI == nil {
		// An error occurred because client_id or redirect_uri are invalid:
		// the caller must display an error page and don't redirect.
		return err
	}

	// 5. Add the response data to the URL and redirect.
	query := redirectURI.Query()
	if req.ResponseType == "code" {
		// Authorization code response
		setQueryPairs(query, "state", req.State)
		var code string
		if err == nil {
			code, err = s.Store.CreateAuthCode(req)
		}
		if err == nil {
			// Success.
			query.Set("code", code)
		} else {
			e := s.InterpretError(err)
			setQueryPairs(query,
				"error", string(e.Code()),
				"error_description", e.Description(),
				"error_uri", e.URI(),
			)
		}
		redirectURI.RawQuery = query.Encode()
		http.Redirect(w, r, redirectURI.String(), 302)

	} else if req.ResponseType == "token" {
		// Implicit Grant Access Token response
		setQueryPairs(query, "state", req.State)
		if err == nil {
			token, token_type, expiry, err := s.Store.CreateImplicitAccessToken(req)
			if err == nil {
				// Success.
				setQueryPairs(query,
					"token", token,
					"token_type", token_type,
					"expires_in", fmt.Sprintf("%d", expiry),
				)
			}
		} else {
			e := s.InterpretError(err)
			setQueryPairs(query,
				"error", string(e.Code()),
				"error_description", e.Description(),
				"error_uri", e.URI(),
			)
		}
		// Encode as fragment
		redirectURI.Fragment = query.Encode()
		http.Redirect(w, r, redirectURI.String(), 302)
	}

	return nil
}

// HandleAccessTokenRequest [...]
func (s *Server) HandleAccessTokenRequest(w http.ResponseWriter, r *http.Request) error {
	// 1. Get all request values.
	req := s.NewAccessTokenRequest(r)

	// 2. Validate required parameters.
	var err error
	// Check for missing or wrong parameters
	if req.GrantType == "" {
		// Missing GrantType: error.
		err = s.NewError(ErrorCodeInvalidRequest,
			"The \"grant_type\" parameter is missing.")
	} else if req.Code == "" {
		// Missing Code: error.
		err = s.NewError(ErrorCodeInvalidRequest,
			"The \"code\" parameter is missing.")
	} else if req.RedirectURI == "" {
		// Missing RedirectURI: error.
		err = s.NewError(ErrorCodeInvalidRequest,
			"The \"redirect_uri\" parameter is missing.")
	} else if req.GrantType != "authorization_code" {
		// GrantType must be authorization_code
		err = s.NewError(ErrorCodeUnsupportedGrantType,
			fmt.Sprintf("The grant type %q is not supported.",
				req.GrantType))
	}

	// 3. Get the response data to the URL.
	// Authorization code response
	var token, token_type string
	var expiry int
	res := make(map[string]string)
	if err == nil {
		token, token_type, expiry, err = s.Store.CreateAccessToken(req)
	}
	if err == nil {
		// Success.
		res["token"] = token
		res["token_type"] = token_type
		res["expires_in"] = fmt.Sprintf("%d", expiry)
	} else {
		e := s.InterpretError(err)
		res["error"] = string(e.Code())
		res["error_description"] = e.Description()
		res["error_uri"] = e.URI()
	}

	// 4. Write the response
	setQueryPairs(w.Header(),
		"Content-Type", "application/json",
		"Cache-Control", "no-store",
		"Pragma", "no-cache",
	)
	encoder := json.NewEncoder(w)
	encoder.Encode(res)

	return nil
}

// VerifyToken
// Validate an Access Token in the request.
// If the request is invalid, return an error
// If the token is valid, return nil
func (s *Server) VerifyToken(r *http.Request) (err error) {
	if authField := r.Header.Get("Authorization"); authField == "" {
		err = s.NewError(ErrorCodeInvalidRequest,
			"The \"Authorization\" header field is missing.")
		return err
	} else if b, e2 := s.Store.ValidateAccessToken(authField); err != nil {
		return s.InterpretError(e2)
	} else if !b {
		err = s.NewError(ErrorCodeInvalidToken,
			"The Access Token is invalid.")
		return err
	}

	// Success
	return nil
}

// Decorate a http.HandlerFunc with an OAuth Access Token Verification
func (server *Server) TokenVerifier(handler http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if err := server.VerifyToken(request); err != nil {
			// Write the error
			response.WriteHeader(http.StatusUnauthorized)
			log.Println("OAuth Handler: Unauthorized access!", err)

			_, err = response.Write([]byte(err.Error()))
			if err != nil {
				log.Println("OAuth Handler: Error writing response!", err)
			}
		} else {
			handler(response, request)
		}
	}
}
