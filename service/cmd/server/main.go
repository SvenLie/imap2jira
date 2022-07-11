package main

import (
	"encoding/base64"
	"errors"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-message/mail"
	strip "github.com/grokify/html-strip-tags-go"
	"github.com/microcosm-cc/bluemonday"
	"github.com/robfig/cron/v3"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
)

func main() {
	cronInterval := os.Getenv("CRON")
	c := cron.New()
	c.AddFunc(cronInterval, func() {
		run()
	})
	go c.Start()
	sig := make(chan os.Signal)
	signal.Notify(sig, os.Interrupt, os.Kill)
	<-sig

}

func run() {

	// ============================================================
	// Configuration
	log.Println("Connecting to server...")

	imapServer := os.Getenv("IMAP_SERVER")
	imapServerPort := os.Getenv("IMAP_PORT")
	imapUser := os.Getenv("IMAP_USER")
	imapPassword := os.Getenv("IMAP_PASSWORD")

	jiraUrl := os.Getenv("JIRA_URL")
	jiraUser := os.Getenv("JIRA_USER")
	jiraPassword := os.Getenv("JIRA_PASSWORD")

	// Connect to server
	c, err := client.DialTLS(imapServer+":"+imapServerPort, nil)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Connected")

	// Don't forget to logout
	defer c.Logout()

	// Login
	if err := c.Login(imapUser, imapPassword); err != nil {
		log.Fatal(err)
	}
	log.Println("Logged in")

	// Select INBOX
	mbox, err := c.Select("INBOX", false)
	if err != nil {
		log.Fatal(err)
	}

	criteria := imap.NewSearchCriteria()
	criteria.WithoutFlags = []string{"\\Seen"}
	uids, err := c.Search(criteria)
	if err != nil {
		log.Println(err)
	}
	log.Println("Message count for INBOX:", len(uids))

	messageSet := new(imap.SeqSet)
	messageSet.AddNum(uids...)

	var section imap.BodySectionName
	items := []imap.FetchItem{section.FetchItem(), imap.FetchEnvelope}
	sanitizePolicy := bluemonday.UGCPolicy()

	messages := make(chan *imap.Message, 1)
	_ = c.Fetch(messageSet, items, messages)

	for message := range messages {
		r := message.GetBody(&section)

		mr, err := mail.CreateReader(r)
		if err != nil {
			log.Fatal(err)
		}

		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			} else if err != nil {
				log.Fatal(err)
			}

			switch p.Header.(type) {
			case *mail.InlineHeader:
				// This is the message's text (can be plain-text or HTML)
				body, _ := ioutil.ReadAll(p.Body)
				plainTextBody := strip.StripTags(string(body))
				plainTextBody = strings.Replace(plainTextBody, "\n", "\\n", -1)
				plainTextBody = strings.Replace(plainTextBody, "\r", "\\r", -1)
				sanitizedBody := sanitizePolicy.Sanitize(plainTextBody)

				content, err := ioutil.ReadFile("structure.json")
				if err != nil {
					log.Fatal(err)
				}

				// Convert []byte to string and print to screen
				jsonString := string(content)
				jsonString = strings.Replace(jsonString, "%SUMMARY%", message.Envelope.Subject, 1)
				jsonString = strings.Replace(jsonString, "%DESCRIPTION%", strings.TrimSpace(sanitizedBody), 1)

				log.Println(jsonString)

				clt := &http.Client{}

				req, err := http.NewRequest("POST", jiraUrl+"/rest/api/latest/issue", strings.NewReader(jsonString))
				req.Header.Add("Content-Type", "application/json")
				req.Header.Add("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(jiraUser+":"+jiraPassword)))
				resp, err := clt.Do(req)
				if err != nil {
					log.Fatal(err)
				}
				defer resp.Body.Close()

				if resp.StatusCode != 201 {
					bodyBytes, err := ioutil.ReadAll(resp.Body)
					if err != nil {
						log.Fatal(err)
					}
					bodyString := string(bodyBytes)
					err = errors.New(bodyString)
					log.Print(err)
				} else {
					delSeqset := new(imap.SeqSet)
					delSeqset.AddRange(mbox.Messages, mbox.Messages)

					flags := []interface{}{imap.SeenFlag}
					if err := c.Store(delSeqset, imap.FormatFlagsOp(imap.AddFlags, true), flags, nil); err != nil {
						log.Println("IMAP Message Flag Update Failed")
						log.Println(err)
						os.Exit(1)
					}

					if err := c.Expunge(nil); err != nil {
						log.Println("IMAP Message mark as Seen Failed")
						os.Exit(1)
					}

					log.Println("Success")
				}
			}
			break
		}
	}

}
