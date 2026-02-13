package dns

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DNSRecord represents a Cloudflare DNS record
type DNSRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl"`
}

// CloudflareResponse represents the API response structure
type CloudflareResponse struct {
	Success bool          `json:"success"`
	Errors  []interface{} `json:"errors"`
	Result  interface{}   `json:"result"`
}

// VerifyDNSOwnership checks if the user has access to the domain via Cloudflare API
func VerifyDNSOwnership(domain, apiToken, zoneID string) (bool, error) {
	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s", zoneID)
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false, err
	}
	
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/json")
	
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	
	if resp.StatusCode == 403 || resp.StatusCode == 401 {
		return false, fmt.Errorf("authentication failed: invalid API token or zone ID")
	}
	
	if resp.StatusCode != 200 {
		return false, fmt.Errorf("API request failed with status: %d", resp.StatusCode)
	}
	
	var cfResp CloudflareResponse
	if err := json.NewDecoder(resp.Body).Decode(&cfResp); err != nil {
		return false, err
	}
	
	if !cfResp.Success {
		return false, fmt.Errorf("cloudflare API returned errors")
	}
	
	return true, nil
}

// GetZoneDomain fetches the domain name associated with a Zone ID
func GetZoneDomain(apiToken, zoneID string) (string, error) {
	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s", zoneID)
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/json")
	
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	
	var cfResp struct {
		Success bool          `json:"success"`
		Errors  []interface{} `json:"errors"`
		Result  struct {
			Name string `json:"name"`
		} `json:"result"`
	}
	
	if err := json.NewDecoder(resp.Body).Decode(&cfResp); err != nil {
		return "", err
	}
	
	if !cfResp.Success {
		return "", fmt.Errorf("failed to fetch zone name: %v", cfResp.Errors)
	}
	
	return cfResp.Result.Name, nil
}

// GetDNSRecord retrieves an existing DNS record for a subdomain
func GetDNSRecord(subdomain, domain, apiToken, zoneID string) (*DNSRecord, error) {
	// If subdomain is already a full domain (contains dots) or domain is empty, use it as-is
	fullDomain := subdomain
	if domain != "" && subdomain != domain && !strings.HasSuffix(subdomain, "."+domain) && !strings.Contains(subdomain, ".") {
		fullDomain = subdomain + "." + domain
	}
	
	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records?type=A&name=%s", zoneID, fullDomain)
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/json")
	
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	
	body, _ := io.ReadAll(resp.Body)
	
	var cfResp struct {
		Success bool          `json:"success"`
		Errors  []interface{} `json:"errors"`
		Result  []DNSRecord   `json:"result"`
	}
	
	if err := json.Unmarshal(body, &cfResp); err != nil {
		return nil, err
	}
	
	if !cfResp.Success {
		return nil, fmt.Errorf("failed to retrieve DNS record: %v", cfResp.Errors)
	}
	
	if len(cfResp.Result) == 0 {
		return nil, nil // No record found
	}
	
	return &cfResp.Result[0], nil
}

// CreateDNSRecord creates a new A record
func CreateDNSRecord(subdomain, domain, ip, apiToken, zoneID string) error {
	// If subdomain is already a full domain (contains dots) or domain is empty, use it as-is
	fullDomain := subdomain
	if domain != "" && subdomain != domain && !strings.HasSuffix(subdomain, "."+domain) && !strings.Contains(subdomain, ".") {
		fullDomain = subdomain + "." + domain
	}
	
	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records", zoneID)
	
	payload := map[string]interface{}{
		"type":    "A",
		"name":    fullDomain,
		"content": ip,
		"ttl":     1, // Auto TTL
		"proxied": false,
	}
	
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/json")
	
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	
	body, _ := io.ReadAll(resp.Body)
	
	var cfResp CloudflareResponse
	if err := json.Unmarshal(body, &cfResp); err != nil {
		return err
	}
	
	if !cfResp.Success {
		return fmt.Errorf("failed to create DNS record: %v", cfResp.Errors)
	}
	
	return nil
}

// UpdateDNSRecord updates an existing DNS record
func UpdateDNSRecord(recordID, ip, apiToken, zoneID string) error {
	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records/%s", zoneID, recordID)
	
	payload := map[string]interface{}{
		"content": ip,
	}
	
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	
	req, err := http.NewRequest("PATCH", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/json")
	
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	
	body, _ := io.ReadAll(resp.Body)
	
	var cfResp CloudflareResponse
	if err := json.Unmarshal(body, &cfResp); err != nil {
		return err
	}
	
	if !cfResp.Success {
		return fmt.Errorf("failed to update DNS record: %v", cfResp.Errors)
	}
	
	return nil
}

// ListDNSRecords lists all A records for a domain
func ListDNSRecords(domain, apiToken, zoneID string) ([]DNSRecord, error) {
	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records?type=A", zoneID)
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/json")
	
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	
	body, _ := io.ReadAll(resp.Body)
	
	var cfResp struct {
		Success bool        `json:"success"`
		Result  []DNSRecord `json:"result"`
	}
	
	if err := json.Unmarshal(body, &cfResp); err != nil {
		return nil, err
	}
	
	if !cfResp.Success {
		return nil, fmt.Errorf("failed to list DNS records")
	}
	
	return cfResp.Result, nil
}

// GetPublicIP detects the server's public IP address
func GetPublicIP() (string, error) {
	resp, err := http.Get("https://api.ipify.org?format=text")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	
	ip, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	
	return strings.TrimSpace(string(ip)), nil
}
