package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"sort"

	"github.com/go-restruct/restruct"
	"github.com/harry1453/go-common-file-dialog/cfd"
	"github.com/harry1453/go-common-file-dialog/cfdutil"
)

type VLQ struct {
	Value uint64
}

// SizeOf implements restruct.Sizer on Vlq.
func (vlq *VLQ) SizeOf() int {
	n := vlq.Value
	result := 1
	for {
		n >>= 7
		if n > 0 {
			result++
		} else {
			return result
		}
	}
}

// Pack implements restruct.Packer on Vlq.
func (vlq *VLQ) Pack(buf []byte, order binary.ByteOrder) ([]byte, error) {
	i := 1
	n := vlq.Value

	buf[0] = uint8(n & 0x7F)
	n >>= 7

	for n != 0 {
		buf[i-1] |= 0x80
		buf[i] = uint8(n & 0x7F)
		n >>= 7
		i++
	}
	return buf[i:], nil
}

// Unpack implements restruct.Packer on Vlq.
func (vlq *VLQ) Unpack(buf []byte, order binary.ByteOrder) ([]byte, error) {
	n := uint64(0)
	shift := 0
	i := 0
	for {
		b := buf[i]
		n |= uint64(b&0x7F) << shift
		shift += 7
		i++
		if b&0x80 == 0 {
			break
		}
	}
	vlq.Value = n
	return buf[i:], nil
}

// Project stores the serialized FL Studio Project data.
type Project struct {
	Chunks []Chunk `struct-while:"!_eof"`
}

// Chunk contains a single IFF chunk.
type Chunk struct {
	Type string `struct:"[4]byte"`
	Len  uint32
	Data struct {
		FLhd    *ChunkFLhd `struct-case:"$'FLhd'"`
		FLdt    *ChunkFLdt `struct-case:"$'FLdt'"`
		Unknown *ChunkUnk  `struct:"default"`
	} `struct-switch:"Type"`
}

// ChunkFLhd contains the FLhd chunk data, which contains a couple of global
// project settings.
type ChunkFLhd struct {
	Parent *Chunk `struct:"parent"`

	Type         uint16
	ChannelCount uint16
	Timebase     uint16
}

// ChunkFLdt contains the FLdt chunk data, which contains the FL project
// represented as a series of events.
type ChunkFLdt struct {
	Parent *Chunk  `struct:"parent"`
	Event  []Event `struct-while:"!_eof"`
}

// ChunkUnk contains an unknown IFF chunk.
type ChunkUnk struct {
	Parent *Chunk `struct:"parent"`
	Data   []byte `struct-size:"Parent.Len"`
}

// Event contains a single FL Studio project event.
type Event struct {
	Type  uint8
	Int8  uint8      `struct-if:"Type & 0xC0 == 0x00"`
	Int16 uint16     `struct-if:"Type & 0xC0 == 0x40"`
	Int32 uint32     `struct-if:"Type & 0xC0 == 0x80"`
	Bytes EventBytes `struct-if:"Type & 0xC0 == 0xC0"`
}

// EventBytes contains a variable-length FL Studio data or text event.
type EventBytes struct {
	Len  VLQ
	Data []byte `struct-size:"Len.Value"`
}

type PlaylistItem struct {
	StartTime   uint32
	PatternBase uint16
	PatternID   uint16
	Length      uint32
	Track       uint32
	Unknown1    uint16
	Unknown2    uint16
	Unknown3    uint32
	Unknown4    uint32
	Unknown5    uint32
}

type PlaylistItemsEvent struct {
	Items []PlaylistItem `struct-while:"!_eof"`
}

var inputFileName string

func init() {
	restruct.EnableExprBeta()

	// Read command line flag.
	flag.Parse()
	inputFileName = flag.Arg(0)
}

func stripext(path string) string {
	for i := len(path) - 1; i >= 0 && !os.IsPathSeparator(path[i]); i-- {
		if path[i] == '.' {
			return path[:i]
		}
	}
	return ""
}

func filterwrite(p Project, track uint32) {
	// Clone the chunks slice.
	p.Chunks = append(p.Chunks[:0:0], p.Chunks...)

	log.Printf("Making project for track %d...", 500-track)
	for i := range p.Chunks {
		log.Printf("0x%02x: Processing %s chunk...", i, p.Chunks[i].Type)
		if p.Chunks[i].Data.FLdt != nil {
			// Clone the FLdt chunk.
			fldt := *p.Chunks[i].Data.FLdt
			fldt.Event = append(fldt.Event[:0:0], fldt.Event...)
			p.Chunks[i].Data.FLdt = &fldt

			log.Printf("0x%02x: Processing FLdt events.", i)
			for j := range p.Chunks[i].Data.FLdt.Event {
				if p.Chunks[i].Data.FLdt.Event[j].Type == 233 {
					var ev PlaylistItemsEvent
					var filteredItems = []PlaylistItem{}
					log.Printf("0x%02x: 0x%08x: Processing playlist event (event 0x%02x).", i, j, p.Chunks[i].Data.FLdt.Event[j].Type)
					err := restruct.Unpack(p.Chunks[i].Data.FLdt.Event[j].Bytes.Data, binary.LittleEndian, &ev)
					if err != nil {
						log.Fatalln(err)
					}
					for k, item := range ev.Items {
						if track != item.Track {
							continue
						}
						log.Printf("0x%02x: 0x%08x: 0x%04x: Adding playlist event item (track %d).", i, j, k, 500-item.Track)
						filteredItems = append(filteredItems, item)
					}
					ev.Items = filteredItems
					log.Printf("0x%02x: 0x%08x: Repacking playlist event.", i, j)
					p.Chunks[i].Data.FLdt.Event[j].Bytes.Data, err = restruct.Pack(binary.LittleEndian, &ev)
					if err != nil {
						log.Fatalln(err)
					}

					// Fixup event length
					log.Printf("0x%02x: 0x%08x: FIXUP: event len was: %d", i, j, p.Chunks[i].Data.FLdt.Event[j].Bytes.Len.Value)
					p.Chunks[i].Data.FLdt.Event[j].Bytes.Len.Value = uint64(len(p.Chunks[i].Data.FLdt.Event[j].Bytes.Data))
					log.Printf("0x%02x: 0x%08x: FIXUP: event len set to: %d", i, j, p.Chunks[i].Data.FLdt.Event[j].Bytes.Len.Value)
					log.Printf("0x%02x: 0x%08x: Finished processing playlist.", i, j)
				}
			}
			log.Printf("0x%02x: Processed %d events successfully.", i, len(p.Chunks[i].Data.FLdt.Event))

			// Fixup chunk length
			log.Printf("0x%02x: FIXUP: chunk len was: %d", i, p.Chunks[i].Len)
			size, err := restruct.SizeOf(p.Chunks[i])
			if err != nil {
				log.Fatalln(err)
			}
			p.Chunks[i].Len = uint32(size) - 8
			log.Printf("0x%02x: FIXUP: chunk len set to: %d", i, p.Chunks[i].Len)
			log.Printf("0x%02x: Finished processing %s chunk.", i, p.Chunks[i].Type)
		}
	}

	outputFilename := fmt.Sprintf("%s-%03d.flp", stripext(inputFileName), 500-track)
	log.Printf("Packing file %s for track %d.", outputFilename, 500-track)

	data, err := restruct.Pack(binary.LittleEndian, &p)
	if err != nil {
		log.Fatalln(err)
	}

	err = ioutil.WriteFile(outputFilename, data, 0644)
	if err != nil {
		log.Fatalf("Writing output failed: %v", err)
	}
}

func main() {
	var err error
	var p Project

	// Try to provide a file picker.
	if inputFileName == "" {
		inputFileName, err = cfdutil.ShowOpenFileDialog(cfd.DialogConfig{
			Title: "Open FL Studio Project File",
			Role:  "OpenFLPFile",
			FileFilters: []cfd.FileFilter{
				{
					DisplayName: "FL Studio Project (*.flp)",
					Pattern:     "*.flp",
				},
				{
					DisplayName: "All Files (*.*)",
					Pattern:     "*.*",
				},
			},
			FileName:         "project.flp",
			DefaultExtension: "flp",
		})
		if err != nil {
			log.Printf("Error trying to display file picker: %v", err)
			log.Print("Try passing a filename via the command line, or dropping a file onto the executable.")
			log.Print("(Press enter or Ctrl+C to exit.)")
			fmt.Scanln()
		}
		if inputFileName == "" {
			fmt.Printf("Usage: %s [project.flp]", os.Args[0])
			return
		}
	}

	file, err := os.Open(inputFileName)
	if err != nil {
		log.Fatalf("Opening project file failed: %v", err)
	}
	defer file.Close()
	data, err := ioutil.ReadAll(file)
	if err != nil {
		log.Fatalf("Reading project file failed: %v", err)
	}

	err = restruct.Unpack(data, binary.LittleEndian, &p)
	if err != nil {
		log.Fatalf("Parsing project file failed: %v", err)
	}

	trackMap := map[int]struct{}{}

	for i, chunk := range p.Chunks {
		log.Printf("0x%02x: Reading %s chunk...", i, chunk.Type)
		if chunk.Data.FLdt != nil {
			log.Printf("0x%02x: Reading FLdt events.", i)
			for j, event := range chunk.Data.FLdt.Event {
				if event.Type == 129 {
					log.Fatalf("0x%02x: 0x%08x: Detected event 0x%02x (Playlist Event.) This FLP is likely too old.", i, j, event.Type)
				}
				if event.Type == 233 {
					var ev PlaylistItemsEvent

					log.Printf("0x%02x: 0x%08x: Parsing playlist event (event 0x%02x).", i, j, event.Type)
					err = restruct.Unpack(event.Bytes.Data, binary.LittleEndian, &ev)
					if err != nil {
						log.Fatalf("Parsing playlist data event failed: %v", err)
					}
					for _, item := range ev.Items {
						trackMap[500-int(item.Track)] = struct{}{}
					}
				}
			}
			log.Printf("0x%02x: Read %d events successfully.", i, len(chunk.Data.FLdt.Event))
		}
	}

	// Sort a list and start writing output files.
	log.Printf("OK. Found %d distinct playlist tracks.", len(trackMap))
	trackList := []int{}
	for track := range trackMap {
		trackList = append(trackList, track)
	}
	sort.Ints(trackList)
	for _, track := range trackList {
		filterwrite(p, uint32(500-track))
	}

	log.Print("Finished.")
	log.Print("(Press enter or Ctrl+C to exit.)")
	fmt.Scanln()
}
