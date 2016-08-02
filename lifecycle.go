package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
	"crypto/tls"
	"crypto/x509"
	"io/ioutil"

	log "github.com/Sirupsen/logrus"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/crewjam/ec2cluster"
)

var (
	certFile = flag.String("cert", "/etc/etcd/etcd-peer.pem", "A PEM eoncoded certificate file.")
	keyFile  = flag.String("key", "/etc/etcd/etcd-peer-key.pem", "A PEM encoded private key file.")
	caFile   = flag.String("CA", "/etc/etcd/ca.pem", "A PEM eoncoded CA's certificate file.")
)

// handleLifecycleEvent is invoked whenever we get a lifecycle terminate message. It removes
// terminated instances from the etcd cluster.
func handleLifecycleEvent(m *ec2cluster.LifecycleMessage) (shouldContinue bool, err error) {
	if m.LifecycleTransition != "autoscaling:EC2_INSTANCE_TERMINATING" {
		return true, nil
	}

	// Loading Security Assets
	cert, err := tls.LoadX509KeyPair(*certFile, *keyFile)
	if err != nil {
		log.Fatal(err)
	}

	// Load CA cert
	caCert, err := ioutil.ReadFile(*caFile)
	if err != nil {
		log.Fatal(err)
	}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	// Setup HTTPS client
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caCertPool,
	}
	tlsConfig.BuildNameToCertificate()
	transport := &http.Transport{TLSClientConfig: tlsConfig}
	client := &http.Client{Transport: transport}

	// look for the instance in the cluster
	resp, err := client.Get(fmt.Sprintf("%s/v2/members", etcdLocalURL))
	if err != nil {
		return false, err
	}
	members := etcdMembers{}
	if err := json.NewDecoder(resp.Body).Decode(&members); err != nil {
		return false, err
	}
	memberID := ""
	for _, member := range members.Members {
		if member.Name == m.EC2InstanceID {
			memberID = member.ID
		}
	}

	if memberID == "" {
		log.WithField("InstanceID", m.EC2InstanceID).Warn("received termination event for non-member")
		return true, nil
	}

	log.WithFields(log.Fields{
		"InstanceID": m.EC2InstanceID,
		"MemberID":   memberID}).Info("removing from cluster")
	req, _ := client.NewRequest("DELETE", fmt.Sprintf("%s/v2/members/%s", etcdLocalURL, memberID), nil)
	_, err = client.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}

	return false, nil
}

func watchLifecycleEvents(s *ec2cluster.Cluster, localInstance *ec2.Instance) {
	etcdLocalURL = fmt.Sprintf("https://%s:2379", *localInstance.PrivateIpAddress)
	for {
		err := s.WatchLifecycleEvents(handleLifecycleEvent)

		// The lifecycle hook might not exist yet if we're being created
		// by cloudformation.
		if err == ec2cluster.ErrLifecycleHookNotFound {
			log.Printf("WARNING: %s", err)
			time.Sleep(10 * time.Second)
			continue
		}
		if err != nil {
			log.Fatalf("ERROR: WatchLifecycleEvents: %s", err)
		}
		panic("not reached")
	}
}
