package main

import (
	"fmt"
	"html"
	"io"
	"log"
	"mime"
	"net"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/boomlinde/dpi"
	"github.com/boomlinde/gemini/client"
	"github.com/boomlinde/gemini/gemini"
)

var tofuLocation string
var gem *client.Client

const pinprefix = "gemini:pin:"
const inputprefix = "gemini:input:"

func main() {
	log.SetPrefix("[dillo-gemini]")

	usr, err := user.Current()
	if err != nil {
		log.Fatal(err)
	}
	gem = client.NewClient(filepath.Join(usr.HomeDir, ".dillo", "gemini", "pinned"))
	gem.Dialer = &net.Dialer{
		Timeout: 30 * time.Second,
	}

	err = dpi.AutoRun(func(tag map[string]string, w io.Writer) error {
		// The writer handed to us from AutoRun will never return an error.
		// It will fail silently and ignore writes after the first error.
		switch tag["cmd"] {
		case "open_url":
			url := tag["url"]

			// Handle special pin URI
			if strings.HasPrefix(url, pinprefix) {
				topin := url[len(pinprefix):]
				log.Println("pinning certificate for", topin)
				if err := gem.Pin(topin); err != nil {
					interrpage(w, url, err)
					return err
				}
				redirect(w, url, topin)
			}

			// Handle special input URI
			if strings.HasPrefix(url, inputprefix) {
				return handleInput(w, url[len(inputprefix):])
			}

			// Normalize the URL (somewhat; net/url is too relaxed)
			norm, err := normalized(url)
			if err != nil {
				interrpage(w, url, err)
				return err
			}
			url = norm

			// Handle connection errors
			r, err := gem.Request(url)
			if err != nil {
				if client.Untrusted(err) || client.Invalid(err) {
					pinpage(w, url, err)
					return dpi.Done
				}
				interrpage(w, url, err)
				return err
			}
			defer r.Close()

			header, err := client.GetHeader(r)
			if err != nil {
				interrpage(w, url, err)
				return err
			}
			switch header.Code / 10 {
			case 1:
				if header.Code == 11 {
					inputpage(w, url, "password", header.Meta)
				} else {
					inputpage(w, url, "text", header.Meta)
				}
			case 2:
				mime, params, err := mime.ParseMediaType(header.Meta)
				if err != nil {
					interrpage(w, url, fmt.Errorf("failed to parse media type: %w", err))
					return err
				}

				// We only support a limited number of charsets.
				if charset, ok := params["charset"]; ok {
					charset = strings.ToLower(charset)
					if charset != "us-ascii" && charset != "utf-8" {
						err := fmt.Errorf("unsupported encoding: %s", charset)
						interrpage(w, url, err)
					}
				}

				if mime == "text/gemini" {
					lines, err := gemini.Itemize(r)
					if err != nil {
						interrpage(w, url, fmt.Errorf("failed to parse text/gemini: %w", err))
						return err
					}

					startPage(w, url, "text/html")
					w.Write([]byte("<!DOCTYPE html>\n"))
					w.Write([]byte("<html>\n"))
					w.Write([]byte("<head>\n"))
					w.Write([]byte("<title>"))
					w.Write([]byte(html.EscapeString(url)))
					w.Write([]byte("</title>\n"))
					w.Write([]byte("</head>\n"))
					w.Write([]byte("<body id='gemini-plugin-body'>\n"))
					w.Write([]byte("<div>\n"))
					// error can never happen here
					_ = gemini.ToHtml(lines, w)
					w.Write([]byte("</div>\n"))
					w.Write([]byte("</body>\n"))
					w.Write([]byte("</html>\n"))
					return dpi.Done
				}

				startPage(w, url, header.Meta)
				if _, err := io.Copy(w, r); err != nil {
					interrpage(w, url, fmt.Errorf("failed to write content: %w", err))
					return err
				}
			case 3:
				redirect(w, url, header.Meta)
			default:
				startPage(w, url, "text/plain")
				fmt.Fprintf(w, "%d %s\n", header.Code, header.Meta)
			}

			return dpi.Done
		case "DpiBye":
			os.Exit(0)
		}
		return nil
	})
	if err != dpi.Done {
		log.Fatal(err)
	}
}

func redirect(w io.Writer, from, to string) {
	startPage(w, from, "text/html")
	w.Write([]byte("<!DOCTYPE html>\n"))
	w.Write([]byte("<html>\n"))
	w.Write([]byte("<head>\n"))
	w.Write([]byte("<title>"))
	fmt.Fprintf(w, "Redirecting to %s", html.EscapeString(to))
	w.Write([]byte("</title>\n"))
	fmt.Fprintf(w, "<meta http-equiv=\"Refresh\" content=\"0; url='%s'\" />", to)
	w.Write([]byte("</head>\n"))
	w.Write([]byte("<body id='gemini-plugin-body'>\n"))
	w.Write([]byte("</body>\n"))
	w.Write([]byte("</html>\n"))
}

func pinpage(w io.Writer, uri string, tlserr error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		panic(err)
	}
	startPage(w, parsed.String(), "text/html")
	w.Write([]byte("<!DOCTYPE html>\n"))
	w.Write([]byte("<html>"))
	fmt.Fprintf(w, "<head><title>Pin %s</title></head>", html.EscapeString(parsed.String()))
	w.Write([]byte("<body id='gemini-plugin-body'>"))
	w.Write([]byte("<h2>Suspicious or unknown certificate</h2>"))
	fmt.Fprintf(w, "<p><b>%s</b></p>", html.EscapeString(tlserr.Error()))
	fmt.Fprintf(w, "<p><a href='gemini:pin:%s'>Pin %s and continue</a></p>", parsed, html.EscapeString(parsed.Hostname()))
	w.Write([]byte("</body>"))
	w.Write([]byte("</html>"))
}
func interrpage(w io.Writer, uri string, err error) {
	startPage(w, uri, "text/html")
	w.Write([]byte("<!DOCTYPE html>\n"))
	w.Write([]byte("<html>"))
	w.Write([]byte("<head><title>Error</title></head>"))
	w.Write([]byte("<body id='gemini-plugin-body'>"))
	fmt.Fprintf(w, "<h2>Error on %s</h2>", html.EscapeString(uri))
	fmt.Fprintf(w, "<p><b>error: %s</b></p>", html.EscapeString(err.Error()))
	w.Write([]byte("</body>"))
	w.Write([]byte("</html>"))
}

func inputpage(w io.Writer, uri, typ, desc string) {
	e := html.EscapeString
	startPage(w, uri, "text/html")
	w.Write([]byte("<!DOCTYPE html>\n"))
	w.Write([]byte("<head>\n"))
	w.Write([]byte("<title>"))
	w.Write([]byte(e(uri)))
	w.Write([]byte("</title>\n"))
	w.Write([]byte("</head>\n"))
	w.Write([]byte("<body id='gemini-plugin-body'>\n"))
	w.Write([]byte("<div>\n"))

	fmt.Fprintf(w, "<form action='%s%s' method='get'>\n", inputprefix, uri)
	fmt.Fprintf(w, "<label for='q'>%s</label><br>\n", e(desc))
	fmt.Fprintf(w, "<input type='%s' id='q' name='q'><br>", typ)
	w.Write([]byte("<input type='submit' value='Submit'>"))
	w.Write([]byte("</form>\n"))

	w.Write([]byte("</div>\n"))
	w.Write([]byte("</body>\n"))
	w.Write([]byte("</html>\n"))
}

func handleInput(w io.Writer, u string) error {
	spl := strings.SplitN(u, "?q=", 2)
	if len(spl) != 2 {
		return fmt.Errorf("malformed query: %s", u)
	}
	target := spl[0]
	query, err := url.QueryUnescape(spl[1])
	if len(spl) != 2 {
		return fmt.Errorf("failed to unescape query: %w", err)
	}

	// hack to encode query with %20 instead of + for spaces
	query = (&url.URL{Path: query}).String()
	// hack to encode + as %2B
	query = strings.Replace(query, "+", "%2B", -1)

	redirect(w, inputprefix+u, target+"?"+query)
	return dpi.Done
}

func startPage(w io.Writer, url, mime string) {
	dpi.Tag(w, map[string]string{"cmd": "start_send_page", "url": url})
	fmt.Fprintf(w, "Content-Type: %s\r\n\r\n", mime)
}

func normalized(in string) (string, error) {
	u, err := url.Parse(in)
	if err != nil {
		return "", err
	}

	// RFC 3986 6.2.3
	//  In general, a URI that uses the generic syntax for authority
	//  with an empty path should be normalized to a path of "/".
	if u.Path == "" && u.Host != "" {
		u.Path = "/"
	}

	// Likewise, an explicit ":port", for which the port is empty or
	// the default for the scheme, is equivalent to one where the port
	// and its ":" delimiter are elided and thus should be removed by
	// scheme-based normalization.
	if strings.HasSuffix(u.Host, ":") {
		u.Host = u.Host[:len(u.Host)-1]
	}

	return u.String(), nil
}
