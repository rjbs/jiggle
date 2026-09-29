use v5.36;

use Test::More;

use lib 'lib';

use Jiggle::FlickrLinks qw();

my $rewriter = Jiggle::FlickrLinks->new({
  base_url => 'https://photos.example.com/',
  photos   => {
    54629694208 => '5d269350738b',
    53466831710 => 'aaaa00000001',
    5494444760  => 'aaaa00000002',
  },
  albums   => { 72157604385579385 => 'oslo-qa-hackathon-2008-04' },
});

sub rewrites_to ($desc, $in, $want, $want_problems = 0) {
  my ($out, $changes, $problems) = $rewriter->rewrite($in);
  is($out, $want, $desc);
  is(scalar @$problems, $want_problems, "$desc: problems reported")
    or diag explain $problems;
}

sub left_alone ($desc, $in, $reason) {
  my ($out, $changes, $problems) = $rewriter->rewrite($in);
  is($out, $in, "$desc: unchanged");
  like(join("\n", map {; $_->[1] } @$problems), $reason, "$desc: reported");
}

rewrites_to('an embed, as in the keyboards post',
  q{<a href="https://www.flickr.com/photos/rjbs/54629694208/in/dateposted-ff/" title="red and blue switches"><img src="https://live.staticflickr.com/65535/54629694208_0f6202b977_c.jpg" width="600" height="800" alt="red and blue switches"/></a>},
  q{{% photo 5d269350738b %}});

rewrites_to('an embed across lines, with a short link, as in the bookshelf post',
  qq{<a href="https://flic.kr/p/2psFuj3">\n<img src="https://live.staticflickr.com/65535/53466831710_b2a7637ff6_c.jpg">\n</a>},
  q{{% photo aaaa00000001 %}});

rewrites_to('an embedded video, as in the keyboards post',
  q{<a href="https://www.flickr.com/photos/rjbs/54629694208/in/dateposted-ff/" title="stuck stabs"><video src="https://www.flickr.com/photos/rjbs/54629694208/play/1080p/405daa8fab/" width="450" height="800" poster="https://live.staticflickr.com/31337/54629694208_405daa8fab_c.jpg" controls=""></video></a>},
  q{{% photo 5d269350738b %}});

rewrites_to('a video embed missing its closing tag, as in the nanoleaf post',
  q{<a href="https://www.flickr.com/photos/rjbs/54629694208/in/dateposted/" title="nanoleaf spinner"><video src="https://www.flickr.com/photos/rjbs/54629694208/play/720p/25a6db5549/" poster="https://live.staticflickr.com/31337/54629694208_25a6db5549_c.jpg" controls=""></video>

More text.},
  qq{{% photo 5d269350738b %}

More text.});

rewrites_to('a bare image',
  q{<img src="https://farm6.staticflickr.com/5014/5494444760_4ba67eed86_c.jpg" width="800">},
  q{{% photo aaaa00000002 %}});

rewrites_to('a Markdown image',
  q{see ![alar](https://live.staticflickr.com/5014/5494444760_4ba67eed86_z.jpg)},
  q{see {% photo aaaa00000002 %}});

rewrites_to('a Markdown link to a photo, with an album suffix',
  q{[the switches](https://www.flickr.com/photos/rjbs/54629694208/in/album-72157604385579385/)},
  q{[the switches](https://photos.example.com/p/5d269350738b/)});

rewrites_to('a short link on its own',
  q{photo: https://flic.kr/p/2psFuj3.},
  q{photo: https://photos.example.com/p/aaaa00000001/.});

rewrites_to('an album, in front matter',
  q{      url: https://flickr.com/photos/rjbs/albums/72157604385579385},
  q{      url: https://photos.example.com/albums/oslo-qa-hackathon-2008-04/});

rewrites_to('an album, as an old-style set',
  q{<a href="http://www.flickr.com/photos/rjbs/sets/72157604385579385/">photos</a>},
  q{<a href="https://photos.example.com/albums/oslo-qa-hackathon-2008-04/">photos</a>});

rewrites_to('the photostream',
  q{<a href="https://flickr.com/photos/rjbs/">Photos</a>},
  q{<a href="https://photos.example.com/">Photos</a>});

left_alone('someone else\'s photo',
  q{see https://www.flickr.com/photos/52666286@N00/2976072633/ for more},
  qr/not one of the owner's photos/);

left_alone('a photo not on the new site',
  q{https://www.flickr.com/photos/rjbs/99999999999/},
  qr/isn't published/);

left_alone('an album not on the new site',
  q{https://www.flickr.com/photos/rjbs/albums/1234/},
  qr/album 1234 isn't published/);

left_alone('an embed whose link and image disagree',
  q{<a href="https://www.flickr.com/photos/rjbs/54629694208/"><img src="https://live.staticflickr.com/65535/53466831710_b2a7637ff6_c.jpg"></a>},
  qr/the link is to 54629694208 but the image is of 53466831710/);

is(Jiggle::FlickrLinks::decode_short_id('2psFuj3'), 53466831710, 'short ids decode');

done_testing;
