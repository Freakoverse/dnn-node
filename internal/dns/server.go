package dns

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"dnn-node/internal/config"
	"dnn-node/internal/database"
	"dnn-node/internal/encoder"

	"github.com/miekg/dns"
	"github.com/sirupsen/logrus"
)

// Server represents the DNS server
type Server struct {
	config       *config.Config
	db           *database.Database
	encoder      *encoder.Encoder
	dnsServer    *dns.Server
	dnsTCPServer *dns.Server
}

// NewServer creates a new DNS server
func NewServer(cfg *config.Config, db *database.Database) (*Server, error) {
	server := &Server{
		config:  cfg,
		db:      db,
		encoder: encoder.NewEncoderWithNetwork(cfg.Network),
	}

	// Create DNS server
	mux := dns.NewServeMux()
	mux.HandleFunc(".", server.handleDNSRequest)

	server.dnsServer = &dns.Server{
		Addr:    fmt.Sprintf("0.0.0.0:%d", cfg.DNS.Port),
		Net:     "udp",
		Handler: mux,
	}

	server.dnsTCPServer = &dns.Server{
		Addr:    fmt.Sprintf("0.0.0.0:%d", cfg.DNS.Port),
		Net:     "tcp",
		Handler: mux,
	}

	return server, nil
}

// Start starts the DNS server
// Returns true if started successfully, false if binding failed (e.g., permission denied)
func (s *Server) Start() bool {
	logrus.Infof("Starting DNN DNS server on port %d (UDP and TCP)", s.config.DNS.Port)

	// Try to bind UDP first to check if we have permission
	udpAddr := fmt.Sprintf("0.0.0.0:%d", s.config.DNS.Port)
	udpConn, err := net.ListenPacket("udp", udpAddr)
	if err != nil {
		logrus.Warnf("⚠️  DNS server could not bind to port %d: %v", s.config.DNS.Port, err)
		logrus.Warn("   DNN name resolution is still available via HTTP API at /dnn/resolve/{name}")
		if s.config.DNS.Port == 53 {
			logrus.Warn("   To enable DNS: Run with administrator/root privileges")
		}
		return false
	}
	udpConn.Close()

	// Try TCP as well
	tcpAddr := fmt.Sprintf("0.0.0.0:%d", s.config.DNS.Port)
	tcpListener, err := net.Listen("tcp", tcpAddr)
	if err != nil {
		logrus.Warnf("⚠️  DNS TCP server could not bind to port %d: %v", s.config.DNS.Port, err)
		return false
	}
	tcpListener.Close()

	// Start UDP server
	go func() {
		if err := s.dnsServer.ListenAndServe(); err != nil {
			logrus.Errorf("UDP DNS server failed: %v", err)
		}
	}()

	// Start TCP server
	go func() {
		if err := s.dnsTCPServer.ListenAndServe(); err != nil {
			logrus.Errorf("TCP DNS server failed: %v", err)
		}
	}()

	logrus.Info("✓ DNS server started successfully")
	return true
}

// Stop stops the DNS server
func (s *Server) Stop() error {
	logrus.Info("Stopping DNS servers")
	s.dnsServer.Shutdown()
	s.dnsTCPServer.Shutdown()
	return nil
}

// handleDNSRequest handles incoming DNS requests
func (s *Server) handleDNSRequest(w dns.ResponseWriter, r *dns.Msg) {
	msg := &dns.Msg{}
	msg.SetReply(r)
	msg.Authoritative = false

	if len(r.Question) == 0 {
		msg.SetRcode(r, dns.RcodeFormatError)
		w.WriteMsg(msg)
		return
	}

	question := r.Question[0]
	domain := strings.ToLower(question.Name)
	qtype := question.Qtype

	if strings.HasSuffix(domain, ".") {
		domain = domain[:len(domain)-1]
	}

	logrus.Debugf("DNS query: %s (type: %s)", domain, dns.TypeToString[qtype])

	if s.isDNNName(domain) {
		s.handleDNNDomain(w, r, msg, domain, qtype)
	} else {
		s.forwardToUpstream(w, r, msg, domain, qtype)
	}
}

// isDNNName checks if a name looks like a DNN name (with or without subdomain)
func (s *Server) isDNNName(name string) bool {
	// Check for subdomain.dnnname pattern
	parts := strings.Split(name, ".")
	if len(parts) >= 2 {
		// Check if the last part (base name) is a DNN name
		baseName := parts[len(parts)-1]
		if len(baseName) > 1 && baseName[0] == 'n' {
			for _, r := range baseName[1:] {
				if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
					return true
				}
			}
		}
	}

	// Check for plain DNN name (no subdomain)
	if len(name) > 1 && name[0] == 'n' {
		for _, r := range name[1:] {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				return true
			}
		}
	}
	return false
}

// handleDNNDomain handles DNS queries for DNN domains (with subdomain support)
func (s *Server) handleDNNDomain(w dns.ResponseWriter, r *dns.Msg, msg *dns.Msg, domain string, qtype uint16) {
	// Extract subdomain and base DNN name
	subdomain, baseName := s.extractSubdomain(domain)

	// Decode the base name to get block and position
	block, pos, err := s.encoder.Decode(baseName)
	if err != nil {
		logrus.Warnf("Failed to decode DNN name %s: %v", baseName, err)
		msg.SetRcode(r, dns.RcodeNameError)
		w.WriteMsg(msg)
		return
	}

	// Check if this DNN ID is blocked by node operator
	isBlocked, err := s.db.IsBlocked(block, pos, subdomain)
	if err == nil && isBlocked {
		logrus.Warnf("DNS blocked for DNN ID n%d.%d (name=%s) [operator block]", block, pos, baseName)
		msg.SetRcode(r, dns.RcodeNameError) // NXDOMAIN
		w.WriteMsg(msg)
		return
	}

	// Determine which section to use (primary name key or subdomain name)
	primaryName, pnErr := s.db.GetPrimaryNameByBlockAndPosition(block, pos)
	if pnErr != nil || primaryName == "" {
		logrus.Warnf("Failed to get primary name for block=%d pos=%d: %v", block, pos, pnErr)
		msg.SetRcode(r, dns.RcodeNameError)
		w.WriteMsg(msg)
		return
	}
	sectionName := primaryName
	if subdomain != "" {
		// If subdomain matches the primary name, keep the primary name key
		if !strings.EqualFold(subdomain, primaryName) {
			sectionName = subdomain
		}
	}

	// Query database for connection content
	connectionContent, err := s.db.GetConnectionContentByBlockAndPosition(block, pos)
	if err != nil {
		logrus.Warnf("Failed to get connection for %s (block=%d, pos=%d): %v", baseName, block, pos, err)
		msg.SetRcode(r, dns.RcodeNameError)
		w.WriteMsg(msg)
		return
	}

	// Parse connection content to extract DNS records for the specific section
	records, err := s.parseConnectionContentSection(connectionContent, sectionName)
	if err != nil {
		logrus.Errorf("Failed to parse connection content for %s (section=%s): %v", domain, sectionName, err)
		msg.SetRcode(r, dns.RcodeServerFailure)
		w.WriteMsg(msg)
		return
	}

	// Convert records to DNS responses
	dnsRecords, err := s.convertToDNSRecords(domain, records, qtype)
	if err != nil || len(dnsRecords) == 0 {
		msg.SetRcode(r, dns.RcodeNameError)
		w.WriteMsg(msg)
		return
	}

	for _, record := range dnsRecords {
		msg.Answer = append(msg.Answer, record)
	}

	msg.Authoritative = true
	logrus.Infof("Resolved %s (section=%s) to %d records", domain, sectionName, len(dnsRecords))
	w.WriteMsg(msg)
}

// parseConnectionContent parses the connection JSON content for a specific domain name
func (s *Server) parseConnectionContent(content string, domainName string) ([][]string, error) {
	return s.parseConnectionContentSection(content, domainName)
}

// parseConnectionContentSection parses a specific section of connection JSON
func (s *Server) parseConnectionContentSection(content string, sectionName string) ([][]string, error) {
	var connData map[string]struct {
		Records [][]string `json:"records"`
	}

	if err := json.Unmarshal([]byte(content), &connData); err != nil {
		return nil, fmt.Errorf("failed to parse connection JSON: %w", err)
	}

	section, ok := connData[sectionName]
	if !ok {
		return nil, fmt.Errorf("section '%s' not found in connection data", sectionName)
	}

	return section.Records, nil
}

// extractSubdomain extracts subdomain and base name from a domain
// Returns (subdomain, baseName)
// Examples:
//
//	"nabceabsurd" -> ("", "nabceabsurd")
//	"alice.nabceabsurd" -> ("alice", "nabceabsurd")
//	"alice_work.nabceabsurd" -> ("alice_work", "nabceabsurd")
func (s *Server) extractSubdomain(domain string) (string, string) {
	parts := strings.Split(domain, ".")
	if len(parts) == 1 {
		// No subdomain
		return "", domain
	}
	// Last part is the base DNN name, everything before is the subdomain
	baseName := parts[len(parts)-1]
	subdomain := strings.Join(parts[:len(parts)-1], ".")
	return subdomain, baseName
}

// mapSubdomainToSection maps a subdomain to a connection section name
// If subdomain matches the primary name, returns the primary name (domain key)
// Otherwise returns the subdomain itself as the section name
func (s *Server) mapSubdomainToSection(block int64, position int, subdomain string) (string, error) {
	// Get the primary name from database
	primaryName, err := s.db.GetPrimaryNameByBlockAndPosition(block, position)
	if err != nil {
		return "", fmt.Errorf("failed to get primary name: %w", err)
	}

	// If subdomain matches the primary name, use it as the key
	if strings.EqualFold(subdomain, primaryName) {
		return primaryName, nil
	}

	// Otherwise, use the subdomain as the section name
	return subdomain, nil
}

// forwardToUpstream forwards DNS queries to upstream servers
func (s *Server) forwardToUpstream(w dns.ResponseWriter, r *dns.Msg, msg *dns.Msg, domain string, qtype uint16) {
	client := &dns.Client{Timeout: time.Duration(s.config.DNS.QueryTimeout) * time.Second}

	for _, upstreamAddr := range s.config.DNS.UpstreamDNS {
		resp, _, err := client.Exchange(r, upstreamAddr)
		if err != nil {
			continue
		}
		w.WriteMsg(resp)
		return
	}

	msg.SetRcode(r, dns.RcodeServerFailure)
	w.WriteMsg(msg)
}

// convertToDNSRecords converts DNN records to DNS RR
func (s *Server) convertToDNSRecords(domain string, records [][]string, qtype uint16) ([]dns.RR, error) {
	var result []dns.RR

	for _, record := range records {
		if len(record) < 3 {
			continue
		}

		recordType := strings.ToUpper(record[0])
		if qtype != dns.TypeANY && !s.matchesQueryType(recordType, qtype) {
			continue
		}

		rr, err := s.convertSingleRecord(domain, record)
		if err == nil && rr != nil {
			result = append(result, rr)
		}
	}

	return result, nil
}

// matchesQueryType checks if a record type matches the DNS query type
func (s *Server) matchesQueryType(recordType string, qtype uint16) bool {
	switch recordType {
	case "A":
		return qtype == dns.TypeA
	case "AAAA":
		return qtype == dns.TypeAAAA
	case "CNAME":
		return qtype == dns.TypeCNAME
	case "TXT":
		return qtype == dns.TypeTXT
	case "MX":
		return qtype == dns.TypeMX
	case "SRV":
		return qtype == dns.TypeSRV
	default:
		return false
	}
}

// convertSingleRecord converts a single DNN record to dns.RR
func (s *Server) convertSingleRecord(domain string, record []string) (dns.RR, error) {
	recordType := strings.ToUpper(record[0])
	name := record[1]

	fullName := domain
	if name != "@" && name != "" {
		fullName = name + "." + domain
	}
	if !strings.HasSuffix(fullName, ".") {
		fullName += "."
	}

	ttl := uint32(3600)
	if len(record) > 3 && record[3] != "" {
		if parsed, err := strconv.ParseUint(record[3], 10, 32); err == nil {
			ttl = uint32(parsed)
		}
	}

	header := dns.RR_Header{
		Name:   fullName,
		Rrtype: s.getRecordType(recordType),
		Class:  dns.ClassINET,
		Ttl:    ttl,
	}

	switch recordType {
	case "A":
		ip := net.ParseIP(record[2])
		if ip == nil || ip.To4() == nil {
			return nil, fmt.Errorf("invalid IPv4")
		}
		return &dns.A{Hdr: header, A: ip.To4()}, nil

	case "AAAA":
		ip := net.ParseIP(record[2])
		if ip == nil || ip.To4() != nil {
			return nil, fmt.Errorf("invalid IPv6")
		}
		return &dns.AAAA{Hdr: header, AAAA: ip}, nil

	case "CNAME":
		target := record[2]
		if !strings.HasSuffix(target, ".") {
			target += "."
		}
		return &dns.CNAME{Hdr: header, Target: target}, nil

	case "TXT":
		return &dns.TXT{Hdr: header, Txt: []string{record[2]}}, nil

	case "MX":
		if len(record) < 4 {
			return nil, fmt.Errorf("invalid MX")
		}
		priority, _ := strconv.ParseUint(record[2], 10, 16)
		mx := record[3]
		if !strings.HasSuffix(mx, ".") {
			mx += "."
		}
		return &dns.MX{Hdr: header, Preference: uint16(priority), Mx: mx}, nil

	case "SRV":
		if len(record) < 6 {
			return nil, fmt.Errorf("invalid SRV")
		}
		priority, _ := strconv.ParseUint(record[2], 10, 16)
		weight, _ := strconv.ParseUint(record[3], 10, 16)
		port, _ := strconv.ParseUint(record[4], 10, 16)
		target := record[5]
		if !strings.HasSuffix(target, ".") {
			target += "."
		}
		return &dns.SRV{
			Hdr:      header,
			Priority: uint16(priority),
			Weight:   uint16(weight),
			Port:     uint16(port),
			Target:   target,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported type")
	}
}

// getRecordType converts string record type to DNS type constant
func (s *Server) getRecordType(recordType string) uint16 {
	switch recordType {
	case "A":
		return dns.TypeA
	case "AAAA":
		return dns.TypeAAAA
	case "CNAME":
		return dns.TypeCNAME
	case "TXT":
		return dns.TypeTXT
	case "MX":
		return dns.TypeMX
	case "SRV":
		return dns.TypeSRV
	default:
		return dns.TypeNone
	}
}
