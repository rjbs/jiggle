use v5.36;

use Test::More;

use lib 'lib', 't/lib';

use JSON::MaybeXS ();
use XML::LibXML;
use XML::LibXML::XPathContext;
use Jiggle::Photo;
use Jiggle::Site;
use Jiggle::TestLibrary;

sub site_files ($dir) {
  my @files;
  my $iter = $dir->iterator({ recurse => 1 });
  while (my $f = $iter->()) {
    push @files, $f->relative($dir)->stringify unless $f->is_dir;
  }
  return sort @files;
}

sub never_mentioned_ok ($desc, $dir, $needle) {
  my @hits = grep {; $dir->child($_)->slurp_raw =~ /\Q$needle/ } site_files($dir);
  is_deeply(\@hits, [], "$desc: no published file mentions $needle");
  ok(! -e $dir->child("p/$needle"), "$desc: no directory for $needle");
}

sub location_published_is ($desc, $site, $photo, $want) {
  is_deeply(scalar $site->public_location($photo), $want, "location: $desc");
}

# A public photo and an unpublishable one (with the given attributes) are
# built, in an album that lists the unpublishable one first, so it would be
# the default cover.  Nothing of the unpublishable one may appear.
sub never_published_ok ($desc, %unpublishable) {
  subtest "never published: $desc" => sub {
    my ($site, $dir) = built_site(
      photos => [
        { id => 'pub00001', taken => '2026-07-17T10:00:00+02:00', tags => [ 'vienna' ],
          location => { lat => 48.2, lon => 16.37 } },
        { id => 'priv0001', taken => '2026-07-18T10:00:00+02:00', tags => [ 'vienna', 'secret' ],
          location => { lat => 48.21, lon => 16.38 }, %unpublishable },
      ],
      albums => [
        { slug => 'trip', title => 'Trip', photos => [ 'priv0001', 'pub00001' ] },
      ],
    );

    ok(-e $dir->child('p/pub00001/index.html'), 'public photo has a page');
    like($dir->child('map/photos.geojson')->slurp_raw, qr/pub00001/, 'public photo is on the map');
    like($dir->child('albums/index.html')->slurp_raw, qr/pub00001/, 'public photo is the album cover');
    never_mentioned_ok($desc, $dir, 'priv0001');
    ok(! -e $dir->child('tags/secret'), 'tag used only by an unpublishable photo has no page');
  };
}

never_published_ok('private', visibility => 'private');
never_published_ok('pending', pending => 1);
never_published_ok('pending and private', pending => 1, visibility => 'private');

subtest 'a photo whose renditions are missing is left out' => sub {
  my $photos = [
    { id => 'good0001', taken => '2016-11-27T14:00:00' },
    { id => 'bad00001', taken => '2016-11-27T14:21:25' },
  ];
  my $albums = [ { slug => 'melb', title => 'Melbourne', photos => [ 'bad00001', 'good0001' ] } ];

  # As when an original couldn't be rendered: derive never recorded it.
  {
    my ($library, $root) = library_with(photos => $photos, albums => $albums, unrendered => [ 'bad00001' ]);

    my $site = Jiggle::Site->new({ library => $library });
    ok(eval { $site->build; 1 }, 'never rendered: the build finishes') or diag $@;

    my $dir = $root->child('site');
    ok(-e $dir->child('p/good0001/index.html'), 'never rendered: the good photo is published');
    never_mentioned_ok('never rendered', $dir, 'bad00001');
  }

  # A rendition deleted by hand isn't noticed unless verifying.
  {
    my ($library, $root) = library_with(photos => $photos, albums => $albums);
    $library->derived_path('bad00001', 'h480.webp')->remove;

    my $site = Jiggle::Site->new({ library => $library, verify => 1 });
    ok(eval { $site->build; 1 }, 'deleted, with verify: the build finishes') or diag $@;
    never_mentioned_ok('deleted, with verify', $root->child('site'), 'bad00001');
  }
};

subtest 'albums with only private photos are omitted' => sub {
  my ($site, $dir) = built_site(
    photos => [ { id => 'priv0002', visibility => 'private' } ],
    albums => [ { slug => 'hidden', title => 'Hidden', photos => [ 'priv0002' ] } ],
  );

  ok(! -e $dir->child('albums/hidden'), 'no album page');
  never_mentioned_ok('empty album', $dir, 'hidden/');
};

sub site_with_config ($config) {
  my ($library) = library_with(config => $config, photos => []);
  return Jiggle::Site->new({ library => $library });
}

sub at ($lat, $lon, %extra) {
  Jiggle::Photo->new({
    id => 'x', location => { lat => $lat, lon => $lon, %extra },
    original => { ext => 'jpg', width => 1, height => 1 },
  });
}

my $ZONE = qq{[[private_zone]]\nlat = 40.0\nlon = -75.0\nradius = 500\n};

subtest 'private zones' => sub {
  my $site = site_with_config($ZONE);

  location_published_is('inside zone',  $site, at(40.001, -75.001), undef);
  location_published_is('outside zone', $site, at(40.01, -75.0), { lat => 40.01, lon => -75.0 });
  location_published_is('marked private, far from any zone', $site,
    at(48.2, 16.37, private => 1), undef);
};

subtest 'published locations are rounded' => sub {
  my $site = site_with_config('');

  location_published_is('to 3 places by default', $site,
    at(48.2084123, 16.3731456), { lat => 48.208, lon => 16.373 });

  location_published_is('south and west', $site,
    at(-37.8098306, -144.9615472), { lat => -37.81, lon => -144.962 });

  location_published_is('to a configured precision', site_with_config("location_precision = 2\n"),
    at(48.2084123, 16.3731456), { lat => 48.21, lon => 16.37 });

  # This photo is 489m from the zone's center, just inside its 500m radius.
  # Rounded to 2 places, its latitude would be 778m away, outside the zone,
  # so the zone check must use the true location.
  location_published_is('rounding never moves a photo out of a zone',
    site_with_config("location_precision = 2\n[[private_zone]]\nlat = 40.003\nlon = -75.0\nradius = 500\n"),
    at(40.0074, -75.0), undef);
};

subtest 'only rounded coordinates reach the published site' => sub {
  my ($site, $dir) = built_site(
    photos => [ { id => 'vienna01', location => { lat => 48.2084123, lon => 16.3731456 } } ],
  );

  for my $page ('map/photos.geojson', 'p/vienna01/index.html') {
    my $text = $dir->child($page)->slurp_raw;
    like($text,   qr/48\.208\b/,  "$page has the rounded latitude");
    unlike($text, qr/48\.2084/,   "$page lacks the precise latitude");
    unlike($text, qr/16\.3731/,   "$page lacks the precise longitude");
  }
};

subtest 'located photos inside a private zone stay off the map' => sub {
  my ($site, $dir) = built_site(
    config => $ZONE,
    photos => [ { id => 'home0001', location => { lat => 40.0001, lon => -75.0001 } } ],
  );

  unlike($dir->child('map/photos.geojson')->slurp_raw, qr/home0001/, 'not in GeoJSON');
  unlike($dir->child('p/home0001/index.html')->slurp_raw, qr/jiggleMap/, 'no map on page');
};

subtest 'rebuilding changes nothing, and pruning removes the stale' => sub {
  my ($site, $dir, $library) = built_site(
    photos => [ { id => 'aaaa0001' }, { id => 'bbbb0001' } ],
  );

  my $page  = $dir->child('p/aaaa0001/index.html');
  my $mtime = (stat $page)[9];
  utime $mtime - 100, $mtime - 100, $page;

  my $rebuild = Jiggle::Site->new({ library => $library });
  $rebuild->build;

  is((stat $page)[9], $mtime - 100, 'unchanged page was not rewritten');
  is($rebuild->writer->stats->{written}, 0, 'nothing written on rebuild');
  ok($rebuild->writer->trusting_manifest, 'the rebuild went by the manifest');

  # A photo removed from the library: the manifest knows its files.
  $library->meta_path('bbbb0001')->remove;
  Jiggle::Site->new({ library => Jiggle::Library->new({ root => $library->root }) })->build;
  ok(! -e $dir->child('p/bbbb0001'), "a removed photo's files and directory are pruned");

  # A stray file, of the kind left by a build that stopped before saving its
  # manifest.  --verify walks the tree, so it's found.
  $dir->child('p/zzzz0001/index.html')->touchpath;
  Jiggle::Site->new({ library => $library, verify => 1 })->build;
  ok(! -e $dir->child('p/zzzz0001'), 'with verify, a stray file is pruned');
};

sub embed_of ($dir, $id) {
  my $file = $dir->child('p', $id, 'embed.json');
  return -e $file ? JSON::MaybeXS::decode_json($file->slurp_raw) : undef;
}

subtest 'embed data for other sites' => sub {
  my ($site, $dir) = built_site(
    config => qq{base_url = "https://photos.example.com"\n},
    photos => [
      { id => 'hhhh0001', title => 'red and blue switches', taken => '2025-07-04T14:21:25',
        location => { lat => 40.62, lon => -75.37 } },
      { id => 'hhhh0002', taken => '2025-07-05T09:00:00' },
      { id => 'hhhh0003', type => 'video', title => 'a clip',
        original => { file => 'c.mov', ext => 'mov', sha256 => 'f' x 64, bytes => 1,
                      width => 1080, height => 1920, duration => 7 } },
      { id => 'hhhh0004', title => 'secret', visibility => 'private' },
    ],
  );

  my $embed = embed_of($dir, 'hhhh0001');
  is($embed->{format}, 1, 'versioned');
  is($embed->{title}, 'red and blue switches', 'title');
  is($embed->{alt},   'red and blue switches', 'alt text is the title');
  is($embed->{url},   'https://photos.example.com/p/hhhh0001/', 'absolute page URL');
  is_deeply($embed->{renditions}{'1024.webp'},
    { url => 'https://photos.example.com/p/hhhh0001/1024.webp', width => 1024, height => 768 },
    'a rendition, with its size');
  is($embed->{video}, undef, 'a photo has no video');
  unlike($dir->child('p/hhhh0001/embed.json')->slurp_raw, qr/40\.62|-75\.37/, 'no location');

  is(embed_of($dir, 'hhhh0002')->{alt}, '5 July 2025, 09:00', 'untitled: alt is the date');

  my $video = embed_of($dir, 'hhhh0003');
  is($video->{type}, 'video', 'a video says so');
  is_deeply($video->{video},
    { url => 'https://photos.example.com/p/hhhh0003/video.mp4', width => 1080, height => 1920 },
    '...and has its video');

  is(embed_of($dir, 'hhhh0004'), undef, 'a private photo has no embed data');
};

# The feed's entries, as [ kind-and-id, title ] in feed order.
sub feed_entries_of ($dir) {
  my $doc = XML::LibXML->load_xml(location => $dir->child('feed.xml') . '');
  my $xpc = XML::LibXML::XPathContext->new($doc);
  $xpc->registerNs(a => 'http://www.w3.org/2005/Atom');
  return map {;
    [ $xpc->findvalue('a:id', $_) =~ s{\Ahttps://photos\.example\.com}{}r, $xpc->findvalue('a:title', $_) ]
  } $xpc->findnodes('/a:feed/a:entry');
}

subtest 'the feed' => sub {
  my ($site, $dir) = built_site(
    config => qq{base_url = "https://photos.example.com"\n},
    photos => [
      # In an album, so represented by it.
      { id => 'iiii0001', title => 'in the album', added => '2026-09-01T10:00:00-04:00' },
      # On its own, newer than the album.
      { id => 'iiii0002', title => 'loose, new', added => '2026-09-20T10:00:00-04:00',
        tags => [ 'high-st' ], location => { lat => 40.62, lon => -75.37 } },
      # On its own, older, dated only by its Flickr upload.
      { id => 'iiii0003', title => 'loose, old', flickr_uploaded => '2008-01-06T21:32:33-05:00' },
      # Undated, so left out.
      { id => 'iiii0004', title => 'undated' },
      # Private, so left out.
      { id => 'iiii0005', title => 'secret', added => '2026-09-25T10:00:00-04:00', visibility => 'private' },
    ],
    albums => [
      { slug => 'trip', title => 'The Trip', photos => [ 'iiii0001' ], created => '2026-09-02T10:00:00-04:00' },
    ],
  );

  is_deeply(
    [ feed_entries_of($dir) ],
    [
      [ '/p/iiii0002/', 'loose, new' ],
      [ '/albums/trip/', 'The Trip' ],
      [ '/p/iiii0003/', 'loose, old' ],
    ],
    'albums and loose photos, newest first; album members, undated, and private left out',
  );

  my $xml = $dir->child('feed.xml')->slurp_utf8;
  like($xml, qr{<category term="high-st"/>}, 'tags are categories');
  like($xml, qr{https://photos\.example\.com/p/iiii0002/1024\.webp}, 'a photo entry shows the photo');
  unlike($xml, qr/secret|iiii0005/, 'nothing of the private photo');
  unlike($xml, qr/40\.62|-75\.37/, 'no location');
  like($dir->child('index.html')->slurp_utf8, qr{<link rel="alternate" type="application/atom\+xml" href="/feed\.xml"},
    'pages link to it');
};

subtest 'loose photos are grouped by the day they were taken' => sub {
  my ($site, $dir) = built_site(
    config => qq{base_url = "https://photos.example.com"\n},
    photos => [
      # Three from one day, added at different times: one entry, dated by
      # the latest addition.
      { id => 'llll0001', title => 'the Deltron show', taken => '2026-01-22T21:00:00',
        added => '2026-01-23T09:00:00-05:00', tags => [ 'music' ] },
      { id => 'llll0002', title => 'the Deltron show', taken => '2026-01-22T21:05:00',
        added => '2026-01-23T09:00:00-05:00' },
      { id => 'llll0003', title => 'the encore', taken => '2026-01-22T22:30:00',
        added => '2026-02-01T09:00:00-05:00' },
      # Another day: an entry of its own, as a single photo.
      { id => 'llll0004', title => 'the next morning', taken => '2026-01-23T08:00:00',
        added => '2026-01-24T09:00:00-05:00' },
      # No taken date: grouped by the day added.
      { id => 'llll0005', title => 'mystery one', added => '2026-01-25T09:00:00-05:00' },
      { id => 'llll0006', title => 'mystery two', added => '2026-01-25T10:00:00-05:00' },
    ],
  );

  is_deeply(
    [ feed_entries_of($dir) ],
    [
      [ 'tag:photos.example.com,2026:day/taken/2026-01-22', 'the Deltron show, and 2 more' ],
      [ 'tag:photos.example.com,2026:day/added/2026-01-25', 'mystery one, and 1 more' ],
      [ '/p/llll0004/', 'the next morning' ],
    ],
    'a day of photos is one entry; a day of one photo is that photo',
  );

  my $xml = $dir->child('feed.xml')->slurp_utf8;
  like($xml, qr{<published>2026-02-01T09:00:00-05:00</published>}, 'a day is dated by its latest addition');
  like($xml, qr{3 photos from 22 January 2026}, 'it says how many, and when');
  like($xml, qr{href="https://photos\.example\.com/2026/01/"}, 'and links to the month');
  like($xml, qr{<category term="music"/>}, "its photos' tags are its categories");
};

subtest "the feed's discovery link names the site plainly" => sub {
  my ($site, $dir) = built_site(config => qq{title = "rjbs's <photos> & \\"more\\""\n}, photos => []);
  like($dir->child('index.html')->slurp_utf8,
    qr{href="/feed\.xml" title="rjbs's &lt;photos&gt; &amp; &quot;more&quot;"},
    'the apostrophe is left alone; the rest is escaped');
};

subtest 'the feed holds at most 30 entries' => sub {
  # Each taken on a different day, so each is an entry of its own.
  my ($site, $dir) = built_site(
    photos => [ map {;
      {
        id    => sprintf('jjjj%04d', $_),
        taken => sprintf('2026-%02d-%02dT10:00:00', 1 + int($_ / 28), 1 + $_ % 28),
        added => sprintf('2026-09-01T10:%02d:00-04:00', $_),
      }
    } 1 .. 31 ],
  );

  my @entries = feed_entries_of($dir);
  is(scalar @entries, 30, 'thirty entries');
  is($entries[0][0], '/p/jjjj0031/', 'the newest first');
  is($entries[-1][0], '/p/jjjj0002/', 'the oldest dropped');
};

subtest 'an impossible date leaves only that item undated' => sub {
  my ($site, $dir) = built_site(
    photos => [
      { id => 'kkkk0001', added => '2026-09-31T10:00:00-04:00' },   # no 31 September
      { id => 'kkkk0002', added => '2026-09-30T10:00:00-04:00' },
    ],
  );

  is_deeply([ map {; $_->[0] } feed_entries_of($dir) ], [ '/p/kkkk0002/' ], 'the build goes on');
};

subtest 'a base URL with a trailing slash' => sub {
  my ($site, $dir) = built_site(
    config => qq{base_url = "https://photos.example.com/"\n},
    photos => [ { id => 'gggg0001' } ],
  );

  like($dir->child('p/gggg0001/index.html')->slurp_utf8,
    qr{<meta property="og:url" content="https://photos\.example\.com/p/gggg0001/">},
    'no doubled slash');
};

subtest 'albums are listed newest first' => sub {
  my ($site, $dir) = built_site(
    photos => [ { id => 'ffff0001' } ],
    albums => [
      { slug => 'older',   title => 'Older',   photos => [ 'ffff0001' ], created => '2008-01-07T10:00:00-05:00' },
      # Sorting first by slug, so a missing date would shift the others.
      { slug => 'aaa-undated', title => 'Undated', photos => [ 'ffff0001' ] },
      { slug => 'newer',   title => 'Newer',   photos => [ 'ffff0001' ], created => '2008-01-07T11:00:00-05:00' },
      # Earlier on the clock, but in a later zone: it's the newest instant.
      { slug => 'newest',  title => 'Newest',  photos => [ 'ffff0001' ], created => '2008-01-07T09:30:00-08:00' },
    ],
  );

  my @order = $dir->child('albums/index.html')->slurp_utf8 =~ m{href="/albums/([^/"]+)/"}g;
  is_deeply(\@order, [qw( newest newer older aaa-undated )], 'by creation time, undated last');
};

subtest 'a library can be moved' => sub {
  my ($site, $dir, $library) = built_site(photos => [ { id => 'eeee0001' }, { id => 'eeee0002' } ]);

  my $old_root = $library->root;
  my $new_root = $old_root->sibling($old_root->basename . '-moved');
  rename "$old_root", "$new_root" or die "can't move $old_root: $!";

  my $moved = Jiggle::Site->new({ library => Jiggle::Library->new({ root => $new_root }) });
  $moved->build;

  my $stats = $moved->writer->stats;
  ok($moved->writer->trusting_manifest, 'the manifest is still trusted');
  is($stats->{written}, 0, 'nothing written');
  is($stats->{linked},  0, 'nothing relinked');
  is($stats->{pruned},  0, 'nothing pruned');

  my @mentions = grep {; $_->slurp_raw =~ /\Q$old_root/ }
                 grep {; $_->is_file }
                 $new_root->child('.jiggle')->children, $new_root->child('derived', 'manifest.json');
  is_deeply([ map {; $_->basename } @mentions ], [], 'no bookkeeping names the old location');

  rename "$new_root", "$old_root";   # put it back, for the tempdir cleanup
};

subtest 'after an interrupted build, the manifest is not trusted' => sub {
  my ($site, $dir, $library) = built_site(photos => [ { id => 'cccc0001' } ]);

  # A build that got as far as writing a page, then stopped: its marker is
  # left behind, and the page isn't in any manifest.
  my $writer = Jiggle::Site->new({ library => $library })->writer;
  ok($writer->trusting_manifest, 'the stopped build trusted the manifest');
  $dir->child('p/zzzz0002/index.html')->touchpath;

  my $next = Jiggle::Site->new({ library => $library });
  $next->build;
  ok(! $next->writer->trusting_manifest, 'the next build does not');
  ok(! -e $dir->child('p/zzzz0002'), '...so the stray page is pruned');

  my $after = Jiggle::Site->new({ library => $library });
  $after->build;
  ok($after->writer->trusting_manifest, 'and the build after that trusts it again');
};

# The ids linked from a page, in page order, each counted once.
sub photo_ids_on ($file) {
  my %seen;
  return grep {; ! $seen{$_}++ } $file->slurp_utf8 =~ m{href="/p/([^/"]+)/"}g;
}

sub page_lists_ok ($desc, $dir, $page, $want_ids) {
  my $file = $dir->child($page);
  ok(-e $file, "$desc: $page exists") or return;
  is_deeply([ photo_ids_on($file) ], $want_ids, "$desc: photos on $page");
}

sub page_links_ok ($desc, $dir, $page, @hrefs) {
  my $html = $dir->child($page)->slurp_utf8;
  for my $href (@hrefs) {
    like($html, qr/href="\Q$href\E"/, "$desc: $page links to $href");
  }
}

subtest 'archive by year and month' => sub {
  my ($site, $dir) = built_site(
    photos => [
      { id => 'jul1', taken => '2026-07-01T09:00:00+02:00' },
      { id => 'jul2', taken => '2026-07-20T09:00:00+02:00' },
      # Late on the 31st, local time: August in UTC, but filed under July.
      { id => 'jul3', taken => '2026-07-31T23:30:00+02:00' },
      { id => 'aug1', taken => '2026-08-02T09:00:00' },
      { id => 'old1', taken => '2018-08-13T08:59:43-04:00' },
      { id => 'nodt' },
      { id => 'priv', taken => '2026-07-10T09:00:00+02:00', visibility => 'private' },
    ],
  );

  page_lists_ok('month, oldest first', $dir, '2026/07/index.html', [qw( jul1 jul2 jul3 )]);
  page_lists_ok('next month',          $dir, '2026/08/index.html', [qw( aug1 )]);
  page_lists_ok('older year',          $dir, '2018/08/index.html', [qw( old1 )]);
  page_lists_ok('undated',             $dir, 'archive/undated/index.html', [qw( nodt )]);
  ok(! -e $dir->child('2026/09'), 'no page for a month with no photos');

  page_links_ok('month neighbors', $dir, '2026/07/index.html', '/2026/08/', '/2018/08/', '/2026/');
  page_links_ok('year neighbors',  $dir, '2018/index.html',    '/2026/');
  page_links_ok('archive index',   $dir, 'archive/index.html', '/2026/', '/2018/', '/archive/undated/');
  page_links_ok('photo to month',  $dir, 'p/jul3/index.html',  '/2026/07/');

  never_mentioned_ok('private photo in archive', $dir, 'priv');
};

subtest 'sampling a long month for previews' => sub {
  my ($site) = library_with(photos => []);
  $site = Jiggle::Site->new({ library => $site });

  is_deeply([ $site->sample(3, 1 .. 9) ], [ 1, 4, 7 ], 'evenly spread, in order');
  is_deeply([ $site->sample(12, 1 .. 5) ], [ 1 .. 5 ], 'short lists come back whole');
};

my $RENDER = Jiggle::Site->new({ library => (library_with(photos => []))[0] });

sub description_renders_ok ($desc, $markdown, %want) {
  my $html = $RENDER->description_html($markdown) . '';
  like($html, $_, "$desc: html matches $_")     for ($want{like}   // [])->@*;
  unlike($html, $_, "$desc: html lacks $_")     for ($want{unlike} // [])->@*;
}

sub description_text_is ($desc, $markdown, $want) {
  is($RENDER->excerpt($RENDER->description_text($markdown)), $want, "text: $desc");
}

subtest 'descriptions are Markdown' => sub {
  description_renders_ok('a newline is a line break', "one\ntwo",
    like => [ qr{one<br />\ntwo} ]);

  description_renders_ok('blank lines make paragraphs', "one\n\ntwo",
    like => [ qr{<p>one</p>\n<p>two</p>} ]);

  description_renders_ok('links and emphasis', 'see [the dom](https://example.com/) **now**',
    like => [ qr{<a href="https://example.com/">the dom</a>}, qr{<strong>now</strong>} ]);

  description_renders_ok('raw HTML is omitted', 'a <script>alert(1)</script> b <b>c</b>',
    unlike => [ qr{<script}, qr{<b>} ]);

  description_renders_ok('javascript links are neutered', '[click](javascript:alert(1))',
    unlike => [ qr{javascript:} ]);

  description_text_is('syntax removed', "**Bold** and [a link](https://example.com/).\n\nMore.",
    'Bold and a link. More.');
};

done_testing;
