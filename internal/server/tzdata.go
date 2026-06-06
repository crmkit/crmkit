package server

// Embed the IANA timezone database into the binary. crmkit ships as a static
// CGO_ENABLED=0 binary and runs on minimal containers that may carry no system
// zoneinfo, so time.LoadLocation would only know UTC/Local. Embedding tzdata
// (~450 KB) guarantees workspace timezones like "America/Los_Angeles" resolve
// everywhere the server runs.
import _ "time/tzdata"
