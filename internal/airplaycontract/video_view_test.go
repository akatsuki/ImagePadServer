package airplaycontract
import "testing"
func TestVideoViewMode(t *testing.T){ for _, x:=range []struct{in string; want VideoViewMode}{{"contain",VideoViewContain},{"cover",VideoViewCover}} { got,e:=ParseVideoViewMode(x.in); if e!=nil||got!=x.want{t.Fatalf("%q: %v %v",x.in,got,e)} }; for _,x:=range []string{"","stretch"," CONTAIN ","Contain"}{ if _,e:=ParseVideoViewMode(x); e==nil{t.Fatalf("accepted %q",x)} } }
func TestVideoViewPaths(t *testing.T){ p,e:=VideoViewPathsForReady("x.ready"); if e!=nil||p.Control!="x.ready.video-view.ini"||p.State!="x.ready.video-view.json"{t.Fatal(p,e)}; if _,e=VideoViewPathsForReady(" ");e==nil{t.Fatal("empty accepted")} }
