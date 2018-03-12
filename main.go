// Copyright 2017 Jeff Foley. All rights reserved.
// Use of this source code is governed by Apache 2 LICENSE that can be found in the LICENSE file.

package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strconv"
	"syscall"
	"time"

	"github.com/caffix/amass/amass"
	"github.com/caffix/recon"
)

const (
	defaultWordlistURL = "https://raw.githubusercontent.com/caffix/amass/master/wordlists/namelist.txt"
)

var AsciiArt string = `

        .+++:.            :                             .+++.                   
      +W@@@@@@8        &+W@#               o8W8:      +W@@@@@@#.   oW@@@W#+     
     &@#+   .o@##.    .@@@o@W.o@@o       :@@#&W8o    .@#:  .:oW+  .@#+++&#&     
    +@&        &@&     #@8 +@W@&8@+     :@W.   +@8   +@:          .@8           
    8@          @@     8@o  8@8  WW    .@W      W@+  .@W.          o@#:         
    WW          &@o    &@:  o@+  o@+   #@.      8@o   +W@#+.        +W@8:       
    #@          :@W    &@+  &@+   @8  :@o       o@o     oW@@W+        oW@8      
    o@+          @@&   &@+  &@+   #@  &@.      .W@W       .+#@&         o@W.    
     WW         +@W@8. &@+  :&    o@+ #@      :@W&@&         &@:  ..     :@o    
     :@W:      o@# +Wo &@+        :W: +@W&o++o@W. &@&  8@#o+&@W.  #@:    o@+    
      :W@@WWWW@@8       +              :&W@@@@&    &W  .o#@@W&.   :W@WWW@@&     
        +o&&&&+.                                                    +oooo.                          

                                                  Subdomain Enumeration Tool
                                           Coded By Jeff Foley (@jeff_foley)

`

type outputParams struct {
	Verbose  bool
	Sources  bool
	PrintIPs bool
	FileOut  string
	Results  chan *amass.AmassRequest
	Finish   chan struct{}
	Done     chan struct{}
}

func main() {
	var freq int64
	var wordlist, file, domainsfile string
	var verbose, extra, ip, brute, recursive, whois, list, help bool

	flag.BoolVar(&help, "h", false, "Show the program usage message")
	flag.StringVar(&domainsfile, "domains", "", "Path to the domains file")
	flag.BoolVar(&ip, "ip", false, "Show the IP addresses for discovered names")
	flag.BoolVar(&brute, "brute", false, "Execute brute forcing after searches")
	flag.BoolVar(&recursive, "norecursive", true, "Turn off recursive brute forcing")
	flag.BoolVar(&verbose, "v", false, "Print the summary information")
	flag.BoolVar(&extra, "vv", false, "Print the data source information")
	flag.BoolVar(&whois, "whois", false, "Include domains discoverd with reverse whois")
	flag.BoolVar(&list, "l", false, "List all domains to be used in an enumeration")
	flag.Int64Var(&freq, "freq", 0, "Sets the number of max DNS queries per minute")
	flag.StringVar(&wordlist, "w", "", "Path to a different wordlist file")
	flag.StringVar(&file, "o", "", "Path to the output file")
	flag.Parse()

	if extra {
		verbose = true
	}

	domains := flag.Args()
	if domainsfile != "" {
		domains = append(domains, getFile(domainsfile)...)
	}

	if help || len(domains) == 0 {
		fmt.Println(AsciiArt)
		fmt.Printf("Usage: %s [options] domain domain2 domain3... (e.g. example.com)\n", path.Base(os.Args[0]))
		fmt.Printf("Or just send a file in the options with -domains\n")
		flag.PrintDefaults()
		return
	}

	if whois {
		// Add the domains discovered by whois
		domains = amass.UniqueAppend(domains, recon.ReverseWhois(flag.Arg(0))...)
	}

	if list {
		// Just show the domains and quit
		for _, d := range domains {
			fmt.Println(d)
		}
		return
	}

	// Seed the pseudo-random number generator
	rand.Seed(time.Now().UTC().UnixNano())

	finish := make(chan struct{})
	done := make(chan struct{})
	results := make(chan *amass.AmassRequest, 100)

	go manageOutput(&outputParams{
		Verbose:  verbose,
		Sources:  extra,
		PrintIPs: ip,
		FileOut:  file,
		Results:  results,
		Finish:   finish,
		Done:     done,
	})
	// Execute the signal handler
	go catchSignals(finish, done)
	// Begin the enumeration process
	amass.StartAmass(&amass.AmassConfig{
		Domains:      domains,
		Wordlist:     getWordlist(wordlist),
		BruteForcing: brute,
		Recursive:    recursive,
		Frequency:    freqToDuration(freq),
		Output:       results,
	})
	// Signal for output to finish
	finish <- struct{}{}
	<-done
}

type asnData struct {
	Name      string
	Netblocks map[string]int
}

func manageOutput(params *outputParams) {
	var total int
	var allLines string

	tags := make(map[string]int)
	asns := make(map[int]*asnData)
loop:
	for {
		select {
		case result := <-params.Results: // Collect all the names returned by the enumeration
			total++
			updateData(result, tags, asns)

			var line string
			if params.Sources {
				line += fmt.Sprintf("%-14s", "["+result.Source+"] ")
			}
			if params.PrintIPs {
				line += fmt.Sprintf("%s\n", result.Name+","+result.Address)
			} else {
				line += fmt.Sprintf("%s\n", result.Name)
			}

			// Add line to the others and print it out
			allLines += line
			fmt.Print(line)
		case <-params.Finish:
			break loop
		}
	}
	// Check to print the summary information
	if params.Verbose {
		printSummary(total, tags, asns)
	}
	// Check to output the results to a file
	if params.FileOut != "" {
		ioutil.WriteFile(params.FileOut, []byte(allLines), 0644)
	}
	// Signal that output is complete
	close(params.Done)
}

func updateData(req *amass.AmassRequest, tags map[string]int, asns map[int]*asnData) {
	tags[req.Tag]++

	// Update the ASN information
	data, found := asns[req.ASN]
	if !found {
		asns[req.ASN] = &asnData{
			Name:      req.ISP,
			Netblocks: make(map[string]int),
		}
		data = asns[req.ASN]
	}
	// Increment how many IPs were in this netblock
	data.Netblocks[req.Netblock.String()]++
}

func printSummary(total int, tags map[string]int, asns map[int]*asnData) {
	fmt.Printf("\n%d names discovered - ", total)

	// Print the stats using tag information
	num, length := 1, len(tags)
	for k, v := range tags {
		fmt.Printf("%s: %d", k, v)
		if num < length {
			fmt.Print(", ")
		}
	}
	fmt.Println("")

	// Print a line across the terminal
	for i := 0; i < 8; i++ {
		fmt.Print("----------")
	}
	fmt.Println("")

	// Print the ASN and netblock information
	for asn, data := range asns {
		fmt.Printf("ASN: %d - %s\n", asn, data.Name)

		for cidr, ips := range data.Netblocks {
			s := strconv.Itoa(ips)

			fmt.Printf("\t%-18s\t%-3s ", cidr, s)
			if ips == 1 {
				fmt.Println("IP address")
			} else {
				fmt.Println("IP addresses")
			}
		}
	}
}

// If the user interrupts the program, print the summary information
func catchSignals(output, done chan struct{}) {
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)

	// Wait for a signal
	<-sigs
	// Start final output operations
	output <- struct{}{}
	// Wait for the broadcast indicating completion
	<-done
	os.Exit(0)
}

func getWordlist(path string) []string {
	var list []string

	if path != "" {
		list = getFile(path)
	} else {
		resp, err := http.Get(defaultWordlistURL)
		if err != nil {
			return list
		}
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)

		for scanner.Scan() {
			word := scanner.Text()
			if word != "" {
				list = append(list, word)
			}
		}
	}

	return list
}

func getFile(path string) []string {
	var list []string

	file, err := os.Open(path)
	if err != nil {
		fmt.Printf("Error opening the file: %v\n", err)
		return list
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		word := scanner.Text()
		if word != "" {
			list = append(list, word)
		}
	}
	return list
}

func freqToDuration(freq int64) time.Duration {
	if freq > 0 {
		d := time.Duration(freq)

		if d < 60 {
			// We are dealing with number of seconds
			return (60 / d) * time.Second
		}
		// Make it times per second
		d = d / 60
		m := 1000 / d
		if d < 1000 && m > 1 {
			return m * time.Millisecond
		}
	}
	// Use the default rate
	return amass.DefaultConfig().Frequency
}
