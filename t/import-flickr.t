use v5.36;

use Test::More;

use lib 'lib';

use Jiggle::Import::FlickrBackup;
use Jiggle::Library;
use Path::Tiny ();

plan skip_all => 'vips is needed to make test images'
  unless system('vips --version >/dev/null 2>&1') == 0;

my $tmp = Path::Tiny->tempdir;
my $NSID = '51035772155@N01';

# Write one photo into a fake backup: a small JPEG (distinct per $pixels) and
# an RDF sidecar.  %arg describes the sidecar: its vintage (2.1 spells values
# as element text and has the negative-id bug; 2.2 uses rdf:resource), the id
# as it appears in the file, when it was written, and the photo's metadata.
sub backup_photo ($root, %arg) {
  my $id   = $arg{id};
  my $v21  = ($arg{vintage} // '2.2') eq '2.1';
  my $shown_id = $v21 && $id > 2**31 ? $id - 2**32 : $id;

  my ($y, $m, $d) = $arg{taken} =~ /\A(\d{4})-(\d\d)-(\d\d)/;
  my $dir  = $root->child($y, $m, $d);
  $dir->mkpath;

  my $stem = "$y$m$d-$shown_id-$arg{slug}";
  my $jpg  = $dir->child("$stem.jpg");
  system('vips', 'black', "$jpg", $arg{pixels}, 8) == 0 or die "vips failed";

  my $value = sub ($el, $v) { $v21 ? "<$el>$v</$el>" : qq{<$el rdf:resource="$v"/>} };

  my $tags = join "\n", map {;
    my ($typed, $norm) = @$_;
    qq{<dc:subject>http://www.flickr.com/photos/$NSID/tags/$norm</dc:subject>}
  } ($arg{tags} // [])->@*;

  my $tag_nodes = join "\n", map {;
    my ($typed, $norm) = @$_;
    qq{<flickr:tag rdf:nodeID="httpwwwflickrcomphotos${NSID}tags$norm">}
    . $value->('skos:altLabel', $norm) . $value->('skos:prefLabel', $typed)
    . q{</flickr:tag>}
  } ($arg{tags} // [])->@*;

  my $sets = join "\n", map {;
    qq{<dcterms:isPartOf>http://www.flickr.com/photos/$NSID/sets/$_->{id}</dcterms:isPartOf>}
  } ($arg{sets} // [])->@*;

  my $set_nodes = join "\n", map {;
    qq{<flickr:photoset rdf:nodeID="httpwwwflickrcomphotos${NSID}sets$_->{id}">}
    . qq{<dc:title>$_->{title}</dc:title><dc:description></dc:description></flickr:photoset>}
  } ($arg{sets} // [])->@*;

  $dir->child("$stem.xml")->spew_utf8(<<~"END");
    <rdf:RDF
    xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"
    xmlns:dc="http://purl.org/dc/elements/1.1/"
    xmlns:dcterms="http://purl.org/dc/terms/"
    xmlns:flickr="x-urn:flickr:"
    xmlns:acl="http://www.w3.org/2001/02/acls#"
    xmlns:skos="http://www.w3.org/2004/02/skos/core#"
    xmlns:a="http://www.w3.org/2000/10/annotation-ns"
    >
    <flickr:photo rdf:nodeID="x">
    <dc:title>$arg{title}</dc:title>
    <dc:description>@{[ $arg{description} // '' ]}</dc:description>
    <dc:created>$arg{taken}-0500</dc:created>
    <dc:dateSubmitted>2008-12-01T12:00:00-0500</dc:dateSubmitted>
    $tags
    $sets
    @{[ $value->('acl:accessor', $arg{visibility} // 'public') ]}
    </flickr:photo>
    $tag_nodes
    $set_nodes
    <rdf:Description rdf:nodeID="">
    <dcterms:hasVersion>@{[ $v21 ? '2.1' : '2.2' ]}:$arg{written}</dcterms:hasVersion>
    <a:annotates>http://www.flickr.com/photos/$NSID/$shown_id</a:annotates>
    </rdf:Description>
    </rdf:RDF>
    END

  return $jpg;
}

sub imported ($root, %arg) {
  my $lib = $tmp->child('lib-' . $root->basename);
  $lib->child('jiggle.toml')->touchpath->spew_utf8("format = $Jiggle::Library::FORMAT\n");
  my $library = Jiggle::Library->new({ root => $lib });

  my $summary = Jiggle::Import::FlickrBackup->new({ library => $library, root => $root })->run;

  # A fresh library, to read what was written.
  my $reloaded = Jiggle::Library->new({ root => $lib });
  my %by_flickr = map {; $_->flickr_id => $_ } $reloaded->photos;
  return ($summary, \%by_flickr, [ $reloaded->albums ], $library);
}

sub photo_is ($desc, $photos, $flickr_id, %want) {
  my $photo = $photos->{$flickr_id};
  ok($photo, "$desc: photo $flickr_id imported") or return;
  for my $key (sort keys %want) {
    is_deeply($photo->$key, $want{$key}, "$desc: $key");
  }
}

my $SET = { id => '72157604385579385', title => 'oslo qa hackathon, 2008-04' };

subtest 'a messy backup' => sub {
  my $root = $tmp->child('backup1');

  # A photo backed up in 2009 under its negative id, and again in 2025.  The
  # newer sidecar has the corrected title.
  backup_photo($root, id => 2174183738, vintage => '2.1', written => 1246794059,
    slug => 'martha_box_crawler', title => 'old title', taken => '2008-01-01T10:00:00',
    pixels => 11);
  backup_photo($root, id => 2174183738, vintage => '2.2', written => 1751388459,
    slug => 'martha_box_crawler', title => 'martha, box crawler', taken => '2008-01-01T10:00:00',
    pixels => 11, tags => [ [ 'high-st', 'highst' ] ]);

  # A rename on Flickr left an older pair of files behind.
  backup_photo($root, id => 2173311823, written => 1384628478,
    slug => 'we_ve_got_legs', title => "we've got legs", taken => '2008-01-06T19:36:11',
    pixels => 12);
  backup_photo($root, id => 2173311823, written => 1751388459,
    slug => 'weve_got_legs', title => "we've got legs", taken => '2008-01-06T19:36:11',
    pixels => 12, description => 'we &lt;b&gt;know&lt;/b&gt; how to use them',
    sets => [ $SET ]);

  # Private, in the old spelling, and an album member.
  backup_photo($root, id => 2412116374, vintage => '2.1', written => 1246794059,
    slug => 'secret', title => 'secret', taken => '2008-01-03T09:00:00',
    pixels => 13, visibility => 'private', sets => [ $SET ]);

  my ($summary, $photos, $albums) = imported($root);

  is($summary->{imported}, 3, 'three photos, however many files');
  is_deeply($summary->{skipped}, [], 'nothing skipped');

  photo_is('the newest sidecar wins', $photos, 2174183738,
    title => 'martha, box crawler', tags => [ 'high-st' ], taken => '2008-01-01T10:00:00', visibility => 'public');

  photo_is('description to Markdown; upload time kept', $photos, 2173311823,
    description => 'we **know** how to use them', flickr_uploaded => '2008-12-01T12:00:00-05:00');

  photo_is('private, as spelled by 2.1', $photos, 2412116374, visibility => 'private');

  is(scalar @$albums, 1, 'one album');
  is($albums->[0]->title, $SET->{title}, 'album title');
  is_deeply(
    $albums->[0]->photos,
    [ map {; $photos->{$_}->id } 2412116374, 2173311823 ],
    'no photosets file, so ordered by date taken',
  );
  ok(! defined $photos->{2173311823}->original->{source_mtime}, 'no source mtime on imports');
};

subtest 'album order from a photosets file' => sub {
  my $root = $tmp->child('backup2');
  for my $spec ([ 3001, '2008-04-01T10:00:00' ], [ 3002, '2008-04-02T10:00:00' ], [ 3003, '2008-04-03T10:00:00' ]) {
    backup_photo($root, id => $spec->[0], written => 1751388459, slug => "p$spec->[0]",
      title => "p$spec->[0]", taken => $spec->[1], pixels => $spec->[0] - 2980, sets => [ $SET ]);
  }

  $root->child('photosets', "$SET->{id}.xml")->touchpath->spew_utf8(<<~"END");
    <photoset id="$SET->{id}" primary="3002">
      <title>$SET->{title}</title>
      <photo position="1" id="3003" />
      <photo position="2" id="3001" />
      <photo position="3" id="3002" />
    </photoset>
    END

  my ($summary, $photos, $albums) = imported($root);
  is_deeply($albums->[0]->photos, [ map {; $photos->{$_}->id } 3003, 3001, 3002 ], 'order from the file');
  is($albums->[0]->cover, $photos->{3002}->id, 'cover from the file');
};

subtest 'importing twice adds nothing' => sub {
  my $root = $tmp->child('backup3');
  backup_photo($root, id => 4001, written => 1751388459, slug => 'once', title => 'once',
    taken => '2008-05-01T10:00:00', pixels => 30, sets => [ $SET ]);

  my (undef, undef, undef, $library) = imported($root);
  my $again = Jiggle::Import::FlickrBackup->new({ library => $library, root => $root })->run;

  is($again->{imported}, 0, 'no new photos');
  is($again->{existing}, 1, 'one already present');
  is(scalar(() = $library->albums_dir->children), 1, 'the album was rewritten, not duplicated');
};

done_testing;
