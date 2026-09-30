package Jiggle::Role::FlickrImporter;
use v5.36;

use Moo::Role;

use Jiggle::Album;
use Jiggle::Ingest;

=head1 NAME

Jiggle::Role::FlickrImporter - what importers of Flickr data have in common

=head1 DESCRIPTION

Whatever the source (a Net::Flickr::Backup archive, or Flickr's own data
export), importing works the same way: each file goes through
L<Jiggle::Ingest/ingest_file> with the metadata the source provides, and then
an album file is written for each Flickr album.  This role provides the
shared parts.

=cut

has library => (is => 'ro', required => 1);
has logger  => (is => 'ro', default  => sub { sub { } });

has _ingest => (
  is => 'lazy',
  default => sub ($self) {
    Jiggle::Ingest->new({ library => $self->library, logger => $self->logger });
  },
);

=method taken_from

  my $taken = $importer->taken_from($flickr_wall_clock, $facts);

Flickr's taken date is a wall-clock time, with no real time zone.  This
prefers the file's own EXIF date when it agrees with Flickr's to the second,
since the EXIF date may carry a real offset.  Otherwise Flickr's is used,
since the taken date may have been corrected there.

=cut

sub taken_from ($self, $flickr, $facts) {
  return $facts->{taken} if defined $facts->{taken}
                        and defined $flickr
                        and index($facts->{taken}, $flickr) == 0;
  return $flickr // $facts->{taken};
}

=method write_albums

  my $n = $importer->write_albums(\@albums);

Each album is a hash of C<flickr_id>, C<title>, C<description>, C<photos>
(jiggle ids, in order), C<cover> (a jiggle id), and optionally C<created> (a
TOML datetime).  An album already in the library with the same C<flickr_id>
is rewritten in place, keeping its slug; a new one gets a slug made from its
title.  It returns the number written.

=cut

sub write_albums ($self, $albums) {
  my %existing   = map {; ($_->flickr_id // '') => $_ } $self->library->albums;
  my %slug_taken = map {; $_->slug => 1 } $self->library->albums;

  $self->library->albums_dir->mkpath;

  my $n = 0;
  for my $spec (sort { $a->{flickr_id} cmp $b->{flickr_id} } @$albums) {
    next unless $spec->{photos}->@*;

    my $slug = $existing{ $spec->{flickr_id} }
             ? $existing{ $spec->{flickr_id} }->slug
             : Jiggle::Album->unique_slug($spec->{title} || "album-$spec->{flickr_id}", \%slug_taken);

    my $album = Jiggle::Album->new({
      slug        => $slug,
      title       => $spec->{title} || "Album $spec->{flickr_id}",
      description => $spec->{description} // '',
      cover       => $spec->{cover} // $spec->{photos}[0],
      photos      => $spec->{photos},
      flickr_id   => $spec->{flickr_id},
      (defined $spec->{created} ? (created => $spec->{created}) : ()),
    });

    $self->library->albums_dir->child("$slug.toml")->spew_utf8($album->as_toml);
    $n++;
  }

  return $n;
}

1;
