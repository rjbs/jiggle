package Jiggle::Import::FlickrBackup;
use v5.36;

use Moo;

use Jiggle::Album;
use Jiggle::Ingest;
use Jiggle::Markdown qw( html_to_markdown );
use Jiggle::Progress;
use Path::Tiny ();
use XML::LibXML;
use XML::LibXML::XPathContext;

=head1 NAME

Jiggle::Import::FlickrBackup - import a Net::Flickr::Backup archive

=head1 SYNOPSIS

  my $import = Jiggle::Import::FlickrBackup->new({
    library => $library,
    root    => '/Volumes/Orange/flickr',
  });

  my $summary = $import->run;

=head1 DESCRIPTION

A Net::Flickr::Backup archive holds, for each photo, the original file and
an RDF/XML sidecar describing it, at F<YYYY/MM/DD/YYYYMMDD-ID-SLUG.EXT>.  If
the backup was made with C<fetch_photosets>, there is also a
F<photosets/ID.xml> for each album, giving its photos in order and its cover.

The archive has some mess that the importer copes with:

=for :list
* Renaming a photo on Flickr changes its filename, and the old files stay.
So files are grouped by the Flickr id found I<inside> each sidecar, and the
newest sidecar wins.  The filename's id is only a cross-check.
* Sidecars come in two vintages, which spell some values as element text and
some as an C<rdf:resource> attribute.  Both are read.
* The time zone offset on Flickr's taken date is not the photo's: it's the
offset of the machine that made the backup.  So the taken date's wall-clock
time is used, with no offset, unless the file's own EXIF date agrees with it
and has a real offset.  Where they disagree, Flickr's wins, since it may
have been corrected there.
* Without F<photosets/> files, an album's order is by date taken, and its
cover is its first photo.

Everything goes through L<Jiggle::Ingest/ingest_file>, so ids, originals,
and file facts are exactly as for any other photo, and importing the same
archive twice adds nothing the second time.

=cut

has library => (is => 'ro', required => 1);

has root => (
  is => 'ro',
  required => 1,
  coerce   => sub ($r) { Path::Tiny::path($r)->absolute },
);

has logger => (is => 'ro', default => sub { sub { } });

has _ingest => (
  is => 'lazy',
  default => sub ($self) {
    Jiggle::Ingest->new({ library => $self->library, logger => $self->logger });
  },
);

my %NS = (
  rdf     => 'http://www.w3.org/1999/02/22-rdf-syntax-ns#',
  rdfs    => 'http://www.w3.org/2000/01/rdf-schema#',
  dc      => 'http://purl.org/dc/elements/1.1/',
  dcterms => 'http://purl.org/dc/terms/',
  flickr  => 'x-urn:flickr:',
  acl     => 'http://www.w3.org/2001/02/acls#',
  skos    => 'http://www.w3.org/2004/02/skos/core#',
  geo     => 'http://www.w3.org/2003/01/geo/wgs84_pos#',
  a       => 'http://www.w3.org/2000/10/annotation-ns',
);

my %MEDIA_EXT = map {; $_ => 1 } qw( jpg jpeg png gif heic mov mp4 m4v avi );

=method run

This imports everything in the archive, then writes an album file for each
photoset, and returns a summary:

  { imported => 780, existing => 3, skipped => [...], albums => 13 }

=cut

sub run ($self) {
  my $sidecars = $self->_newest_sidecars;
  $self->logger->(sprintf 'found %d photo(s) on Flickr', scalar keys %$sidecars);

  my (%summary, %set, %id_for_flickr);
  $summary{$_} = 0 for qw( imported existing );
  $summary{skipped} = [];

  my $progress = Jiggle::Progress->new({
    label  => 'import',
    total  => scalar keys %$sidecars,
    logger => $self->logger,
  });

  for my $flickr_id (sort keys %$sidecars) {
    $progress->tick;
    my $sidecar = $sidecars->{$flickr_id};
    my $meta    = $self->_read_sidecar($sidecar->{file});

    my $media = $self->_media_for($sidecar);
    unless ($media) {
      push $summary{skipped}->@*, "$flickr_id: no media file found";
      next;
    }

    my $result = $self->_ingest->ingest_file($media, {
      record_source_mtime => 0,
      metadata => sub ($facts) { $self->_photo_attributes($meta, $facts) },
    });

    if ($result->{status} eq 'skipped') {
      push $summary{skipped}->@*, "$flickr_id: $result->{reason}";
      next;
    }

    $summary{ $result->{status} eq 'ingested' ? 'imported' : 'existing' }++;
    $id_for_flickr{$flickr_id} = $result->{id};

    for my $set ($meta->{sets}->@*) {
      my $s = $set{ $set->{id} } //= { %$set, members => [] };
      push $s->{members}->@*, {
        id    => $result->{id},
        taken => $meta->{taken} // '',
      };
    }
  }

  $progress->done;

  $summary{albums} = $self->_write_albums(\%set, \%id_for_flickr);
  return \%summary;
}

# Find every sidecar, and keep the newest one for each Flickr id.
sub _newest_sidecars ($self) {
  my %newest;

  my $progress = Jiggle::Progress->new({ label => 'scanning sidecars', logger => $self->logger });

  my $iter = $self->root->iterator({ recurse => 1 });
  while (my $file = $iter->()) {
    next unless $file->basename =~ /\A\d{8}-.*\.xml\z/;
    $progress->tick;

    my $doc = eval { XML::LibXML->load_xml(location => "$file") };
    unless ($doc) {
      $self->logger->("skip $file: not valid XML");
      next;
    }

    my $xpc = _xpc($doc);
    my $flickr_id = _flickr_id($xpc);
    unless ($flickr_id) {
      $self->logger->("skip $file: no Flickr photo id in it");
      next;
    }

    my ($file_id) = $file->basename =~ /\A\d{8}-(-?\d+)-/;
    $file_id = _unwrap_id($file_id) if defined $file_id;
    $self->logger->("warning: $file names $file_id, but describes $flickr_id")
      if $file_id and $file_id ne $flickr_id;

    # hasVersion is "LIBRARY-VERSION:EPOCH", when the sidecar was written.
    my ($written) = _value($xpc, '//dcterms:hasVersion') =~ /:(\d+)\z/;
    $written //= $file->stat->mtime;

    my $have = $newest{$flickr_id};
    $newest{$flickr_id} = { file => $file, written => $written }
      if ! $have or $written > $have->{written};
  }

  $progress->done;
  return \%newest;
}

# The media file for a sidecar: the one with the same name, or failing that
# (when only an older, renamed copy survives) any in the same directory with
# the same date and id.
sub _media_for ($self, $sidecar) {
  my $file = $sidecar->{file};
  (my $stem = $file->basename) =~ s/\.xml\z//;
  my ($prefix) = $stem =~ /\A(\d{8}-+\d+-)/;

  my @candidates = grep {;
    my ($ext) = $_->basename =~ /\.(\w+)\z/;
    $ext and $MEDIA_EXT{ lc $ext };
  } $file->parent->children(qr/\A\Q$prefix\E/);

  my ($exact) = grep {; $_->basename =~ /\A\Q$stem\E\.\w+\z/ } @candidates;
  return $exact // $candidates[0];
}

sub _xpc ($doc) {
  my $xpc = XML::LibXML::XPathContext->new($doc);
  $xpc->registerNs($_ => $NS{$_}) for keys %NS;
  return $xpc;
}

# A value may be written as element text or as an rdf:resource attribute,
# depending on the vintage of the sidecar.
sub _node_value ($node) {
  my $resource = $node->getAttributeNS($NS{rdf}, 'resource');
  return $resource if defined $resource and length $resource;
  return $node->textContent;
}

sub _value ($xpc, $path, $context = undef) {
  my ($node) = $xpc->findnodes($path, $context);
  return $node ? _node_value($node) : '';
}

sub _values ($xpc, $path, $context = undef) {
  map {; _node_value($_) } $xpc->findnodes($path, $context);
}

sub _read_sidecar ($self, $file) {
  my $xpc   = _xpc(XML::LibXML->load_xml(location => "$file"));
  my ($photo) = $xpc->findnodes('//flickr:photo');
  die "no flickr:photo in $file\n" unless $photo;

  my %meta = (
    title       => _value($xpc, 'dc:title', $photo),
    description => html_to_markdown(_value($xpc, 'dc:description', $photo)),
    visibility  => (_value($xpc, 'acl:accessor', $photo) eq 'public' ? 'public' : 'private'),
  );

  $meta{taken}    = _wall_clock(_value($xpc, 'dc:created', $photo));
  $meta{uploaded} = _toml_datetime(_value($xpc, 'dc:dateSubmitted', $photo));

  # The photo names its tags by their normalized form.  The tag as it was
  # typed is on the user's own tag node, whose altLabel is the normalized
  # form.
  my %typed;
  for my $tag ($xpc->findnodes('//flickr:tag')) {
    my $alt  = _value($xpc, 'skos:altLabel', $tag);
    my $pref = _value($xpc, 'skos:prefLabel', $tag);
    $typed{$alt} = $pref if length $alt and length $pref;
  }

  $meta{tags} = [
    map {; $typed{$_} // $_ }
    map {; m{/tags/([^/]+)\z} ? $1 : () }
    _values($xpc, 'dc:subject', $photo)
  ];

  my %set_info;
  for my $set ($xpc->findnodes('//flickr:photoset')) {
    my ($id) = ($set->getAttributeNS($NS{rdf}, 'nodeID') // '') =~ /sets(\d+)\z/;
    next unless $id;
    $set_info{$id} = {
      title       => _value($xpc, 'dc:title', $set),
      description => html_to_markdown(_value($xpc, 'dc:description', $set)),
    };
  }

  $meta{sets} = [
    map {; my ($id) = m{/sets/(\d+)}; $id ? { id => $id, %{ $set_info{$id} // {} } } : () }
    _values($xpc, 'dcterms:isPartOf', $photo)
  ];

  # Newer backups record the location and who may see it.  Older ones have
  # only a place hierarchy, and then EXIF (in the file) is all there is.
  my $point = _value($xpc, 'geo:Point', $photo);
  if (length $point) {
    my ($loc) = $xpc->findnodes(qq{//rdf:Description[\@rdf:about="$point"]});
    if ($loc) {
      my ($lat, $lon) = (_value($xpc, 'geo:lat', $loc), _value($xpc, 'geo:long', $loc));
      $meta{location} = { lat => 0 + $lat, lon => 0 + $lon }
        if length $lat and length $lon;

      my $who = _value($xpc, 'acl:accessor', $loc);
      $meta{location_private} = 1 if length $who and $who ne 'public';
    }
  }

  $meta{flickr_id} = _flickr_id($xpc);
  return \%meta;
}

sub _flickr_id ($xpc) {
  my ($id) = _value($xpc, '//rdf:Description/a:annotates') =~ m{/photos/[^/]+/(-?\d+)\z};
  return defined $id ? _unwrap_id($id) : undef;
}

# Sidecars written by Net::Flickr::RDF 2.1 (in 2009) have negative photo ids,
# and so do their filenames, which have a double dash: the ids were stored in
# a signed 32-bit integer, and ids of 2008 photos are past 2**31.  Adding 2**32
# recovers the real id; in the 2008 backup, every such sidecar has a twin
# with the recovered id.  Ids of later photos are past 2**32, but those were
# all written by later versions, which don't have the bug.  -- claude, 2026-09-27
sub _unwrap_id ($id) {
  return $id < 0 ? $id + 2**32 : $id;
}

# "2008-01-06T19:36:11-0500" to "2008-01-06T19:36:11": the time on the clock,
# without the offset, which describes the backup machine, not the photo.
sub _wall_clock ($datetime) {
  my ($wall) = $datetime =~ /\A(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)/;
  return $wall;
}

# "2008-01-06T21:32:33-0500" to "2008-01-06T21:32:33-05:00", for TOML.
sub _toml_datetime ($datetime) {
  my ($wall, $h, $m) = $datetime =~ /\A(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)([-+]\d\d):?(\d\d)\z/;
  return $wall ? "$wall$h:$m" : undef;
}

sub _photo_attributes ($self, $meta, $facts) {
  # Prefer the file's EXIF date when it agrees with Flickr's to the second,
  # since it may carry a real offset.  Otherwise, Flickr's is used, since the
  # taken date may have been corrected there.
  my $taken = $meta->{taken};
  if (defined $facts->{taken} and defined $taken
      and index($facts->{taken}, $taken) == 0) {
    $taken = $facts->{taken};
  }

  my $location = $meta->{location} // $facts->{location};
  $location = { %$location, private => 1 } if $location and $meta->{location_private};

  return {
    title       => $meta->{title},
    description => $meta->{description},
    tags        => $meta->{tags},
    visibility  => $meta->{visibility},
    flickr_id   => $meta->{flickr_id},
    (defined $taken             ? (taken           => $taken)             : ()),
    (defined $meta->{uploaded}  ? (flickr_uploaded => $meta->{uploaded})  : ()),
    ($location                  ? (location        => $location)          : ()),
  };
}

sub _write_albums ($self, $sets, $id_for_flickr) {
  my %existing = map {; ($_->flickr_id // '') => $_ } $self->library->albums;
  my %slug_taken = map {; $_->slug => 1 } $self->library->albums;

  my $n = 0;
  for my $set_id (sort keys %$sets) {
    my $set = $sets->{$set_id};

    my ($photos, $cover) = $self->_album_order($set_id, $set, $id_for_flickr);

    my $slug = $existing{$set_id} ? $existing{$set_id}->slug
             : _unique_slug($set->{title} || "album-$set_id", \%slug_taken);

    my $album = Jiggle::Album->new({
      slug        => $slug,
      title       => $set->{title} || "Album $set_id",
      description => $set->{description} // '',
      cover       => $cover,
      photos      => $photos,
      flickr_id   => $set_id,
    });

    $self->library->albums_dir->mkpath;
    $self->library->albums_dir->child("$slug.toml")->spew_utf8($album->as_toml);
    $n++;
  }

  return $n;
}

# From photosets/ID.xml if the backup has it, or else by date taken.
sub _album_order ($self, $set_id, $set, $id_for_flickr) {
  my $file = $self->root->child('photosets', "$set_id.xml");

  if (-e $file) {
    my $doc = XML::LibXML->load_xml(location => "$file");
    my @photos = map {; $id_for_flickr->{ $_->getAttribute('id') } // () }
                 sort { $a->getAttribute('position') <=> $b->getAttribute('position') }
                 $doc->findnodes('/photoset/photo');
    my $primary = $doc->documentElement->getAttribute('primary');
    return (\@photos, $id_for_flickr->{ $primary // '' } // $photos[0]);
  }

  my %seen;
  my @photos = map  {; $_->{id} }
               grep {; ! $seen{ $_->{id} }++ }
               sort { $a->{taken} cmp $b->{taken} || $a->{id} cmp $b->{id} }
               $set->{members}->@*;

  return (\@photos, $photos[0]);
}

sub _unique_slug ($title, $taken) {
  my $slug = lc $title;
  $slug =~ s/[^\p{Alnum}]+/-/g;
  $slug =~ s/\A-+|-+\z//g;
  $slug = 'album' unless length $slug;

  my $try = $slug;
  my $n = 1;
  $try = "$slug-" . ++$n while $taken->{$try};

  $taken->{$try} = 1;
  return $try;
}

1;
