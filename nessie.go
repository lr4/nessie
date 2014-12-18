package nessie

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
)

var debug bool

func init() {
	flag.BoolVar(&debug, "debug", false, "log the responses from nessus")
}

// Nessus implements most of the communication with Nessus.
type Nessus struct {
	// client is the HTTP client to use to issue requests to nessus.
	client *http.Client
	// authCookie is the login token returned by nessus upon successful login.
	authCookie string
	apiURL     string
}

// NewNessus will return a new Nessus initialized with a client matching the security parameters.
// if caCertPath is empty, the host certificate roots will be used to check for the validity of the nessus server API certificate.
func NewNessus(apiURL, caCertPath string, ignoreSSLCertsErrors bool) (*Nessus, error) {
	var roots *x509.CertPool
	if len(caCertPath) != 0 {
		roots = x509.NewCertPool()
		rootPEM, err := ioutil.ReadFile(caCertPath)
		if err != nil {
			return nil, err
		}
		ok := roots.AppendCertsFromPEM(rootPEM)
		if !ok {
			return nil, fmt.Errorf("could not append certs from PEM %s", caCertPath)
		}
	}
	return &Nessus{
		apiURL: apiURL,
		client: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: ignoreSSLCertsErrors,
					RootCAs:            roots,
				},
			},
		},
	}, nil
}

func (n *Nessus) doRequest(method string, resource string, data url.Values, wantStatus []int) (resp *http.Response, err error) {
	u, err := url.ParseRequestURI(n.apiURL)
	if err != nil {
		return nil, err
	}
	u.Path = resource
	urlStr := fmt.Sprintf("%v", u)

	req, err := http.NewRequest(method, urlStr, bytes.NewBufferString(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded;charset=utf-8")
	req.Header.Add("Accept", "application/json")
	if n.authCookie != "" {
		req.Header.Add("X-Cookie", fmt.Sprintf("token=%s", n.authCookie))
	}

	resp, err = n.client.Do(req)
	if err != nil {
		return nil, err
	}
	if debug {
		if body, err := httputil.DumpResponse(resp, true); err == nil {
			log.Println(string(body))
		}
	}
	var statusFound bool
	for _, status := range wantStatus {
		if resp.StatusCode == status {
			statusFound = true
			break
		}
	}
	if !statusFound {
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("Unexpected status code, got %d wanted %v (%s)", resp.StatusCode, wantStatus, body)
	}
	return resp, nil
}

// Login will log into nessus with the username and passwords given from the command line flags.
func (n *Nessus) Login(username, password string) error {
	if debug {
		log.Printf("Login into %s\n", n.apiURL)
	}
	data := url.Values{}
	data.Set("username", username)
	data.Set("password", password)

	resp, err := n.doRequest("POST", "/session", data, []int{http.StatusOK})
	if err != nil {
		return err
	}
	reply := &loginResp{}
	if err = json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return err
	}
	n.authCookie = reply.Token
	return nil
}

// Logout will invalidate the current session token.
func (n *Nessus) Logout() error {
	if n.authCookie == "" {
		log.Println("Not logged in, nothing to do to logout...")
		return nil
	}
	if debug {
		log.Println("Logout...")
	}

	if _, err := n.doRequest("DELETE", "/session", nil, []int{http.StatusOK}); err != nil {
		return err
	}
	n.authCookie = ""
	return nil
}

// ServerProperties will return the current state of the nessus instance.
func (n *Nessus) ServerProperties() (*ServerProperties, error) {
	if debug {
		log.Println("Server properties...")
	}

	resp, err := n.doRequest("GET", "/server/properties", nil, []int{http.StatusOK})
	if err != nil {
		return nil, err
	}
	reply := &ServerProperties{}
	if err = json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return nil, err
	}
	return reply, nil
}

// ServerStatus will return the current status of the nessus instance.
func (n *Nessus) ServerStatus() (*ServerStatus, error) {
	if debug {
		log.Println("Server status...")
	}

	resp, err := n.doRequest("GET", "/server/status", nil, []int{http.StatusOK, http.StatusServiceUnavailable})
	if err != nil {
		return nil, err
	}
	reply := &ServerStatus{}
	if err = json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		reply.MustDestroySession = true
	}
	return reply, nil
}

const (
	UserTypeLocal = "local"
	UserTypeLDAP  = "ldap"

	Permissions0   = "0"
	Permissions16  = "16"
	Permissions32  = "32"
	Permissions64  = "64"
	Permissions128 = "128"
)

// CreateUser will register a new user with the nessus instance.
// Name and email can be empty.
func (n *Nessus) CreateUser(username, password, userType, permissions, name, email string) (*User, error) {
	if debug {
		log.Println("Creating new user...")
	}
	data := url.Values{}
	data.Set("username", username)
	data.Set("password", password)
	data.Set("permissions", permissions)
	if name != "" {
		data.Set("name", name)
	}
	if email != "" {
		data.Set("email", email)
	}
	data.Set("type", userType)

	resp, err := n.doRequest("POST", "/users", data, []int{http.StatusOK})
	if err != nil {
		return nil, err
	}
	reply := &User{}
	if err = json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return nil, err
	}
	return reply, nil
}

// ListUsers will return the list of users on this nessus instance.
func (n *Nessus) ListUsers() (*[]User, error) {
	if debug {
		log.Println("Listing users...")
	}

	resp, err := n.doRequest("GET", "/users", nil, []int{http.StatusOK})
	if err != nil {
		return nil, err
	}
	reply := &listUsersResp{}
	if err = json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return nil, err
	}
	return &reply.Users, nil
}

// DeleteUser will remove a user from this nessus instance.
func (n *Nessus) DeleteUser(userID int) error {
	if debug {
		log.Println("Deleting user...")
	}

	_, err := n.doRequest("DELETE", fmt.Sprintf("/users/%d", userID), nil, []int{http.StatusOK})
	return err
}

// SetUserPassword will change the password for the given user.
func (n *Nessus) SetUserPassword(userID int, password string) error {
	if debug {
		log.Println("Changing password of user...")
	}
	data := url.Values{}
	data.Set("password", password)

	_, err := n.doRequest("PUT", fmt.Sprintf("/users/%d/chpasswd", userID), data, []int{http.StatusOK})
	return err
}

// EditUser will edit certain information about a user.
// Any non empty parameter will be set.
func (n *Nessus) EditUser(userID int, permissions, name, email string) (*User, error) {
	if debug {
		log.Println("Editing user...")
	}
	data := url.Values{}
	if permissions != "" {
		data.Set("permissions", permissions)
	}
	if name != "" {
		data.Set("name", name)
	}
	if email != "" {
		data.Set("email", email)
	}

	resp, err := n.doRequest("PUT", fmt.Sprintf("/users/%d", userID), data, []int{http.StatusOK})
	if err != nil {
		return nil, err
	}
	reply := &User{}
	if err = json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return nil, err
	}
	return reply, nil
}

func (n *Nessus) PluginFamilies() ([]PluginFamily, error) {
	if debug {
		log.Println("Getting list of plugin families...")
	}

	resp, err := n.doRequest("GET", "/plugins/families", nil, []int{http.StatusOK})
	if err != nil {
		return nil, err
	}
	var reply []PluginFamily
	if err = json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return nil, err
	}
	return reply, nil
}

func (n *Nessus) FamilyDetails(ID int64) (*FamilyDetails, error) {
	if debug {
		log.Println("Getting details of family...")
	}

	resp, err := n.doRequest("GET", fmt.Sprintf("/plugins/families/%d", ID), nil, []int{http.StatusOK})
	if err != nil {
		return nil, err
	}
	reply := &FamilyDetails{}
	if err = json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return nil, err
	}
	return reply, nil
}

func (n *Nessus) PluginDetails(ID int64) (*PluginDetails, error) {
	if debug {
		log.Println("Getting details plugin...")
	}

	resp, err := n.doRequest("GET", fmt.Sprintf("/plugins/plugin/%d", ID), nil, []int{http.StatusOK})
	if err != nil {
		return nil, err
	}
	reply := &PluginDetails{}
	if err = json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return nil, err
	}
	return reply, nil
}

func (n *Nessus) Scanners() ([]Scanner, error) {
	if debug {
		log.Println("Getting scanners list...")
	}

	resp, err := n.doRequest("GET", "/scanners", nil, []int{http.StatusOK})
	if err != nil {
		return nil, err
	}
	var reply []Scanner
	if err = json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return nil, err
	}
	return reply, nil
}

// AllPlugin wil hammer nessus with 20 workers asking for details of every plugins available and
// feeding them in the returned channel. Gettign all the plugins is slow (usually takes a few
// minutes on a decent machine.
func (n *Nessus) AllPlugins() (chan PluginDetails, error) {
	plugChan := make(chan PluginDetails, 20)

	families, err := n.PluginFamilies()
	if err != nil {
		return nil, err
	}
	idChan := make(chan int64, 20)
	var wgf sync.WaitGroup
	var wgp sync.WaitGroup
	for _, family := range families {
		wgf.Add(1)
		go func(famID int64) {
			defer wgf.Done()
			famDetails, err := n.FamilyDetails(famID)
			if err != nil {
				return
			}
			for _, plugin := range famDetails.Plugins {
				wgp.Add(1)
				idChan <- plugin.ID
			}
		}(family.ID)
	}
	pluginFetcher := func() {
		for {
			id, more := <-idChan
			if !more {
				break
			}
			plugin, err := n.PluginDetails(id)
			if err != nil {
				wgp.Done()
				continue
			}
			plugChan <- *plugin
			wgp.Done()
		}
	}
	// Launch our workers getting individual plugin details.
	for i := 0; i < 10; i++ {
		go pluginFetcher()
	}

	go func() {
		wgf.Wait()
		wgp.Wait()
		close(idChan)
		close(plugChan)
	}()

	return plugChan, nil
}
