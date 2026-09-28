package Jiggle::Import::FlickrExport;
use v5.36;

use Moo;

use DateTime;
use JSON::MaybeXS ();
use Jiggle::Markdown qw( html_to_markdown );
use Jiggle::Progress;
use Path::Tiny ();

with 'Jiggle::Role::FlickrImporter';

=head1 NAME

Jiggle::Import::FlickrExport - import Flickr's own "download all my data" export

=head1 SYNOPSIS

  my $import = Jiggle::Import::FlickrExport->new({
    library => $library,
    root    => '/Volumes/Orange/extracted-export',
  });

  my $summary = $import->run;

=head1 DESCRIPTION

Flickr's data export arrives as a set of zip files, some of metadata and
some of originals.  This expects them unpacked under one root, one directory
per zip, like this:

  metadata/1/photo_2173311823.json    one JSON file per photo
  metadata/1/albums.json              every album, with its photos in order
  photos/7/weve-got-legs_2173311823_o.jpg

The directory names don't matter, only which of C<metadata> and C<photos>
they're under.  Originals are found by the Flickr id in their names, which
come in three forms:

  slug_ID_o.jpg          most photos
  ID_SECRET_o.jpg        photos with no title
  slug_ID.mov            videos

Compared with a Net::Flickr::Backup archive, the export has one format, no
duplicates, album order and covers, coordinates, three levels of privacy,
and the rotation of each photo on Flickr.  It lacks who may see a photo's
location.

A few of its values need care:

=for :list
* C<date_imported> is in US Pacific time, with no zone given.  It's
converted to an instant, as C<flickr_uploaded>.
* C<date_taken> is a wall-clock time, and is used as in
L<Jiggle::Role::FlickrImporter/taken_from>.
* C<geo> latitude and longitude are integers, in millionths of a degree.
* C<rotation> is how far Flickr turns the photo clockwise.  Mostly that's
just what the file's own EXIF orientation calls for, which is applied
anyway, so only the difference is recorded, as C<rotate>: that's a photo
turned by hand on Flickr.
* C<privacy> is "public", "private", or "friend & family".  Anything but
public is imported as private.

=cut

has root => (
  is => 'ro',
  required => 1,
  coerce   => sub ($r) { Path::Tiny::path($r)->absolute },
);

my $JSON = JSON::MaybeXS->new->utf8;

sub _load_json ($file) { $JSON->decode($file->slurp_raw) }

=method looks_like_export

  if (Jiggle::Import::FlickrExport->looks_like_export($dir)) { ... }

This is true if C<$dir> has a F<metadata> directory with photo JSON files in
its subdirectories.

=cut

sub looks_like_export ($class, $dir) {
  my $metadata = Path::Tiny::path($dir)->child('metadata');
  return 0 unless -d $metadata;
  return scalar grep {; $_->is_dir && $_->children(qr/\Aphoto_\d+\.json\z/) } $metadata->children;
}

=method run

This imports every photo that has both a metadata file and an original, then
writes the albums, and returns a summary:

  { imported => 11908, existing => 0, skipped => [...], albums => 219 }

=cut

sub run ($self) {
  my $media = $self->_media_by_flickr_id;
  my @meta  = $self->_metadata_files;
  $self->logger->(sprintf 'found %d photo record(s) and %d original(s)',
    0 + @meta, scalar keys %$media);

  my (%summary, %id_for_flickr);
  $summary{$_} = 0 for qw( imported existing );
  $summary{skipped} = [];

  my $progress = Jiggle::Progress->new({
    label  => 'import',
    total  => scalar @meta,
    logger => $self->logger,
  });

  for my $file (@meta) {
    $progress->tick;

    my $record    = _load_json($file);
    my $flickr_id = $record->{id};

    my $original = $media->{$flickr_id};
    unless ($original) {
      push $summary{skipped}->@*, "$flickr_id: no original in the export";
      next;
    }

    my $result = $self->_ingest->ingest_file($original, {
      record_source_mtime => 0,
      metadata => sub ($facts) { $self->_photo_attributes($record, $facts) },
    });

    if ($result->{status} eq 'skipped') {
      push $summary{skipped}->@*, "$flickr_id: $result->{reason}";
      next;
    }

    $summary{ $result->{status} eq 'ingested' ? 'imported' : 'existing' }++;
    $id_for_flickr{$flickr_id} = $result->{id};

    # Flickr's record is kept as it came, bytes and all, so that whatever
    # the importer doesn't use (comments, people, counts, ...) isn't lost.
    $self->_keep_flickr_record("$flickr_id.json", $file);
  }

  $progress->done;

  $summary{albums} = $self->write_albums($self->_albums(\%id_for_flickr));
  return \%summary;
}

sub _metadata_files ($self) {
  return sort map {; $_->children(qr/\Aphoto_\d+\.json\z/) }
              grep {; $_->is_dir } $self->root->child('metadata')->children;
}

sub _media_by_flickr_id ($self) {
  my %media;
  my $photos = $self->root->child('photos');
  return \%media unless -d $photos;

  for my $dir (grep {; $_->is_dir } $photos->children) {
    for my $file ($dir->children) {
      my $id = _flickr_id_from_name($file->basename);
      unless ($id) {
        $self->logger->("warning: can't find a Flickr id in $file");
        next;
      }

      $self->logger->("warning: two originals for $id: $media{$id} and $file")
        if $media{$id};
      $media{$id} = $file;
    }
  }

  return \%media;
}

sub _flickr_id_from_name ($name) {
  return $1 if $name =~ /\A(\d+)_[0-9a-f]+_o\.\w+\z/;   # ID_SECRET_o.jpg
  return $1 if $name =~ /_(\d+)(?:_o)?\.\w+\z/;         # slug_ID_o.jpg, slug_ID.mov
  return;
}

sub _photo_attributes ($self, $record, $facts) {
  my ($taken) = ($record->{date_taken} // '') =~ /\A(\d{4}-\d\d-\d\d) (\d\d:\d\d:\d\d)\z/
              ? "$1T$2" : undef;

  my $location = $facts->{location};
  if (my ($geo) = ($record->{geo} // [])->@*) {
    $location = {
      lat => $geo->{latitude}  / 1_000_000,
      lon => $geo->{longitude} / 1_000_000,
    };
  }

  # Flickr's rotation includes the turn EXIF calls for, which libvips and
  # ffmpeg apply on their own.  Only the rest is a rotation done by hand.
  my $rotate = (($record->{rotation} // 0) - ($facts->{rotation} // 0)) % 360;

  return {
    title       => $record->{name} // '',
    description => html_to_markdown($record->{description} // ''),
    tags        => [ map {; $_->{tag} } ($record->{tags} // [])->@* ],
    visibility  => (($record->{privacy} // '') eq 'public' ? 'public' : 'private'),
    flickr_id   => "$record->{id}",
    rotate      => $rotate,
    _defined(taken           => $self->taken_from($taken, $facts)),
    _defined(flickr_uploaded => _pacific_to_instant($record->{date_imported})),
    _defined(location        => $location),
  };
}

sub _defined ($key, $value) { defined $value ? ($key => $value) : () }

# "2008-07-04 16:12:06", on a Pacific clock, to "2008-07-04T19:12:06-04:00"
# as seen from here: the same instant, written with this machine's offset
# at that moment, as source_mtime is.
sub _pacific_to_instant ($datetime) {
  my ($y, $mo, $d, $h, $mi, $s) = ($datetime // '')
    =~ /\A(\d{4})-(\d\d)-(\d\d) (\d\d):(\d\d):(\d\d)\z/ or return;

  my $dt = DateTime->new(
    year => $y, month => $mo, day => $d, hour => $h, minute => $mi, second => $s,
    time_zone => 'America/Los_Angeles',
  );
  $dt->set_time_zone('local');

  return $dt->strftime('%Y-%m-%dT%H:%M:%S') . ($dt->strftime('%z') =~ s/(\d\d)\z/:$1/r);
}

sub _albums ($self, $id_for_flickr) {
  my ($file) = grep {; -e } map {; $_->child('albums.json') }
               grep {; $_->is_dir } $self->root->child('metadata')->children;
  return [] unless $file;

  $self->_keep_flickr_record('albums.json', $file);

  my @albums;
  for my $album (_load_json($file)->{albums}->@*) {
    my @photos = map {; $id_for_flickr->{$_} // () } ($album->{photos} // [])->@*;
    my ($cover_flickr) = ($album->{cover_photo} // '') =~ m{/(\d+)/?\z};

    push @albums, {
      flickr_id   => "$album->{id}",
      title       => $album->{title} // '',
      description => html_to_markdown($album->{description} // ''),
      photos      => \@photos,
      cover       => ($cover_flickr ? $id_for_flickr->{$cover_flickr} : undef),
      _defined(created => scalar _epoch_to_datetime($album->{created})),
    };
  }

  return \@albums;
}

# Copy a file from the export into the library's meta/flickr, unchanged.  It's
# written only if different, so re-importing doesn't make noise in git.
sub _keep_flickr_record ($self, $name, $source) {
  my $dest  = $self->library->flickr_dir->child($name);
  my $bytes = $source->slurp_raw;
  return if -e $dest and $dest->slurp_raw eq $bytes;

  $dest->parent->mkpath;
  $dest->spew_raw($bytes);
}

# An epoch time, like albums' "created", to a datetime with this machine's
# offset at that moment.
sub _epoch_to_datetime ($epoch) {
  return unless defined $epoch and $epoch =~ /\A\d+\z/;
  my $dt = DateTime->from_epoch(epoch => $epoch, time_zone => 'local');
  return $dt->strftime('%Y-%m-%dT%H:%M:%S') . ($dt->strftime('%z') =~ s/(\d\d)\z/:$1/r);
}

1;
