package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/DECK36/go-gelf/gelf"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"sync"
)

/*
	http://www.freedesktop.org/software/systemd/man/systemd.journal-fields.html
	https://github.com/Graylog2/graylog2-docs/wiki/GELF
*/
type SystemdJournalEntry struct {
	Cursor                         string `json:"__CURSOR"`
	Realtime_timestamp             int64  `json:"__REALTIME_TIMESTAMP,string"`
	Monotonic_timestamp            string `json:"__MONOTONIC_TIMESTAMP"`
	Boot_id                        string `json:"_BOOT_ID"`
	Transport                      string `json:"_TRANSPORT"`
	Priority                       int32  `json:"PRIORITY,string"`
	Syslog_facility                string `json:"SYSLOG_FACILITY"`
	Syslog_identifier              string `json:"SYSLOG_IDENTIFIER"`
	Message                        string `json:"MESSAGE"`
	Pid                            string `json:"_PID"`
	Uid                            string `json:"_UID"`
	Gid                            string `json:"_GID"`
	Comm                           string `json:"_COMM"`
	Exe                            string `json:"_EXE"`
	Cmdline                        string `json:"_CMDLINE"`
	Systemd_cgroup                 string `json:"_SYSTEMD_CGROUP"`
	Systemd_session                string `json:"_SYSTEMD_SESSION"`
	Systemd_owner_uid              string `json:"_SYSTEMD_OWNER_UID"`
	Systemd_unit                   string `json:"_SYSTEMD_UNIT"`
	Source_realtime_timestamp      string `json:"_SOURCE_REALTIME_TIMESTAMP"`
	Machine_id                     string `json:"_MACHINE_ID"`
	Hostname                       string `json:"_HOSTNAME"`
	Logger                         string `json:"LOGGER"`
	EventId                        string `json:"EVENTID"`
	Exception                      string `json:"EXCEPTION"`
	Exception_type                 string `json:"EXCEPTION_TYPE"`
	Exception_Stacktrace           string `json:"EXCEPTION_STACKTRACE"`
	Inner_exception                string `json:"INNEREXCEPTION"`
	Inner_exception_type           string `json:"INNEREXCEPTION_TYPE"`
	Inner_exception_Stacktrace     string `json:"INNEREXCEPTION_STACKTRACE"`
	Status_code                    string `json:"STATUSCODE"`
	Query_string                   string `json:"QUERYSTRING"`
	Member_id                      string `json:"MEMBERID"`
	Correlation_id                 string `json:"CORRELATIONID"`
	Request_path                   string `json:"REQUESTPATH"`
	Request_id                     string `json:"REQUESTID"`
	FullMessage                    string
}

// Strip date from message-content. Use named subpatterns to override other fields
var messageReplace = map[string]*regexp.Regexp{
	"*":         regexp.MustCompile("^20[0-9][0-9][/\\-][01][0-9][/\\-][0123][0-9] [0-2]?[0-9]:[0-5][0-9]:[0-5][0-9][,0-9]{0-3} "),
	"nginx":     regexp.MustCompile("\\[(?P<Priority>[a-z]+)\\] "),
	"java":      regexp.MustCompile("(?P<Priority>[A-Z]+): "),
	"mysqld":    regexp.MustCompile("^[0-9]+ \\[(?P<Priority>[A-Z][a-z]+)\\] "),
	"searchd":   regexp.MustCompile("^\\[([A-Z][a-z]{2} ){2} [0-9]+ [0-2][0-9]:[0-5][0-9]:[0-5][0-9]\\.[0-9]{3} 20[0-9][0-9]\\] \\[[ 0-9]+\\] "),
	"jenkins":   regexp.MustCompile("^[A-Z][a-z]{2} [01][0-9], 20[0-9][0-9] [0-2]?[0-9]:[0-5][0-9]:[0-5][0-9] [AP]M "),
	"php-fpm":   regexp.MustCompile("^pool [a-z_0-9\\[\\]\\-]+: "),
	"syncthing": regexp.MustCompile("^\\[[0-9A-Z]{5}\\] [0-2][0-9]:[0-5][0-9]:[0-5][0-9] (?P<Priority>INFO): "),
}

var priorities = map[string]int32{
	"emergency": 0,
	"emerg":     0,
	"alert":     1,
	"critical":  2,
	"crit":      2,
	"error":     3,
	"err":       3,
	"warning":   4,
	"warn":      4,
	"notice":    5,
	"info":      6,
	"debug":     7,
}

func (this *SystemdJournalEntry) toGelf() *gelf.Message {
	var extra = map[string]interface{}{
		"Boot_id":                     this.Boot_id,
		"Pid":                         this.Pid,
		"Uid":                         this.Uid,
		"Logger":                      this.Logger,
		"EventId":                     this.EventId,
		"Exception":                   this.Exception,
		"Exception_Type":              this.Exception_type,
		"Exception_Stacktrace":        this.Exception_Stacktrace,
		"Inner_Exception":             this.Inner_exception,
		"Inner_Exception_Type":        this.Inner_exception_type,
		"Inner_Exception_Stacktrace":  this.Inner_exception_Stacktrace,
		"Request_Id":                  this.Request_id,
		"Request_Path":                this.Request_path,
		"Status_Code":                 this.Status_code,
		"Query_String":                this.Query_string,
		"Correlation_Id":              this.Correlation_id,
		"Member_Id":                   this.Member_id,
	}

	// php-fpm refuses to fill identifier
	facility := this.Syslog_identifier
	if "" == facility {
		facility = this.Comm
	}

	if this.isJsonMessage() {
		if err := json.Unmarshal([]byte(this.Message), &extra); err == nil {
			if m, ok := extra["Message"]; ok {
				this.Message = m.(string)
				delete(extra, "Message")
			}

			if f, ok := extra["FullMessage"]; ok {
				this.FullMessage = f.(string)
				delete(extra, "FullMessage")
			}
		}
	} else if -1 != strings.Index(this.Message, "\n") {
		this.FullMessage = this.Message
		this.Message = strings.Split(this.Message, "\n")[0]
	}

	return &gelf.Message{
		Version:  "1.1",
		Host:     this.Hostname,
		Short:    this.Message,
		Full:     this.FullMessage,
		TimeUnix: float64(this.Realtime_timestamp) / 1000 / 1000,
		Level:    this.Priority,
		Facility: facility,
		Extra:    extra,
	}
}

func (this *SystemdJournalEntry) process() {
	// Replace generic timestamp
	this.Message = messageReplace["*"].ReplaceAllString(this.Message, "")

	re := messageReplace[ this.Syslog_identifier ]
	if nil == re {
		re = messageReplace[ this.Comm ]
	}

	if nil == re {
		return
	}

	m := re.FindStringSubmatch(this.Message)
	if m == nil {
		return
	}

	// Store subpatterns in fields
	for idx, key := range re.SubexpNames() {
		if "Priority" == key {
			this.Priority = priorities[strings.ToLower(m[idx])]
		}
	}

	this.Message = re.ReplaceAllString(this.Message, "")
}

func (this *SystemdJournalEntry) send() {
	message := this.toGelf()

	if err := writer.WriteMessage(message); err != nil {
		/*
			UDP is nonblocking, but the os stores an error which GO will return on the next call.
			This means we've already lost a message, but can keep retrying the current one. Sleep to make this less obtrusive
		*/
		fmt.Fprintln(os.Stderr, "Processing paused because of: " +err.Error())
		time.Sleep(SLEEP_AFTER_ERROR)
		this.send()
	}
}

func (this *SystemdJournalEntry) isJsonMessage() bool {
	return len(this.Message) > 64 && this.Message[0] == '{' && this.Message[1] == '"'
}

var (
	pending struct{
		sync.RWMutex
		entry *SystemdJournalEntry
	}
	writer       *gelf.Writer
)

const (
	WRITE_INTERVAL             = 50 * time.Millisecond
	SAMESOURCE_TIME_DIFFERENCE = 100 * 1000
	SLEEP_AFTER_ERROR          = 15 * time.Second
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Pass server:12201 as first argument and append journalctl parameters to use")
		os.Exit(1)
	}

	if w, err := gelf.NewWriter(os.Args[1]); err != nil {
		fmt.Fprintf(os.Stderr, "While connecting to Graylog server: %s\n", err)
		os.Exit(1)
	} else {
		writer = w
	}

	journalArgs := []string{"--all", "--output=json"}
	journalArgs = append(journalArgs, os.Args[2:]...)
	cmd := exec.Command("journalctl", journalArgs...)

	stderr, _ := cmd.StderrPipe()
	go io.Copy(os.Stderr, stderr)
	stdout, _ := cmd.StdoutPipe()
	s := bufio.NewScanner(stdout)

	go writePendingEntry()

	cmd.Start()

	for s.Scan() {
		line := s.Text()

		var entry = &SystemdJournalEntry{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			//fmt.Fprintf(os.Stderr, "Could not parse line, skipping: %s\n", line)
			continue
		}

		entry.process()

		pending.Lock()

		if pending.entry == nil {
			pending.entry = entry
		} else {
			pending.entry.send()
			pending.entry = entry
		}

		pending.Unlock()

		// Prevent saturation and throttling
		time.Sleep(1 * time.Millisecond)
	}

	if err := s.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Error from Scanner: %s\n", err)
		cmd.Process.Kill()
		os.Exit(1)
	}

	cmd.Wait()
	pending.entry.send()
}

func writePendingEntry() {
	var entry *SystemdJournalEntry

	for {
		time.Sleep(WRITE_INTERVAL)

		if pending.entry != nil && (time.Now().UnixNano()/1000-pending.entry.Realtime_timestamp) > SAMESOURCE_TIME_DIFFERENCE {
			pending.Lock()
			entry = pending.entry
			pending.entry = nil
			pending.Unlock()

			entry.send()
		}
	}
}
