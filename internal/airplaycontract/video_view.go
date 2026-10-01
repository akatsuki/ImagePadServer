package airplaycontract

import (
 "errors"
 "strings"
)

type VideoViewMode string
const ( VideoViewContain VideoViewMode = "contain"; VideoViewCover VideoViewMode = "cover" )
func ParseVideoViewMode(raw string) (VideoViewMode, error) {
 switch VideoViewMode(raw) { case VideoViewContain: return VideoViewContain,nil; case VideoViewCover: return VideoViewCover,nil }
 return "", errors.New("invalid video view mode")
}
type VideoViewPaths struct { Control, State string }
func VideoViewPathsForReady(ready string) (VideoViewPaths, error) {
 if strings.TrimSpace(ready)=="" { return VideoViewPaths{}, errors.New("ready path is empty") }
 return VideoViewPaths{Control:ready+".video-view.ini", State:ready+".video-view.json"},nil
}
