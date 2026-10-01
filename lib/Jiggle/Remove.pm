package Jiggle::Remove;
use v5.36;

use Moo;

use Jiggle::Album;
use Jiggle::Derive;
use Jiggle::Photo;

=head1 NAME

Jiggle::Remove - take photos out of a library

=head1 SYNOPSIS

  my $result = Jiggle::Remove->new({ library => $library })->remove(
    [ @ids ],
    { originals => 0 },
  );

=head1 DESCRIPTION

Removing a photo deletes its metadata file, takes it out of every album that
lists it (an album whose cover it was gets its first remaining photo as its
cover), and deletes its renditions.  The changes to F<meta> are committed
together, if it's a git repository.

The original is kept unless C<originals> is true, so that a removal can be
undone by reverting the commit: the original and renditions are where they
were (renditions are remade by the next build).  With C<originals>, the
original is deleted too, and the removal can't be undone from the library.

=cut

has library => (is => 'ro', required => 1);
has logger  => (is => 'ro', default => sub { sub { } });

has derive => (
  is => 'lazy',
  default => sub ($self) { Jiggle::Derive->new({ library => $self->library }) },
);

=method remove

  my $result = $remover->remove(\@ids, \%arg);

This removes the photos, and returns a hash: C<removed>, the photo objects
removed; C<albums>, the slugs of albums changed; C<published>, the ids of
removed photos that were on the site; C<originals>, the paths of originals
deleted; and C<commit>, the commit's abbreviated id, if any.  It dies, having
changed nothing, if any id isn't a photo in the library.

=cut

sub remove ($self, $ids, $arg = {}) {
  my $library = $self->library;

  my @photos;
  for my $id (@$ids) {
    my $file = $library->meta_path($id);
    die "no photo $id in the library\n" unless -e $file;
    push @photos, Jiggle::Photo->from_toml_file($file);
  }

  my %gone = map {; $_->id => 1 } @photos;
  my (@changed, @albums);

  for my $album ($library->albums) {
    next unless grep {; $gone{$_} } $album->photos->@*;

    my @left = grep {; ! $gone{$_} } $album->photos->@*;
    my %arg  = (%$album, photos => \@left);
    delete $arg{cover};
    my $cover = $album->cover;
    $cover = $left[0] unless defined $cover and ! $gone{$cover};
    $arg{cover} = $cover if defined $cover;

    my $file = $library->albums_dir->child($album->slug . '.toml');
    $file->spew_utf8(Jiggle::Album->new(\%arg)->as_toml);
    push @changed, $file;
    push @albums, $album->slug;
    $self->logger->(sprintf 'album %s: removed %d photo(s)', $album->slug,
      $album->photos->@* - @left);
  }

  my @originals;
  for my $photo (@photos) {
    my $meta = $library->meta_path($photo->id);
    $meta->remove;
    push @changed, $meta;

    $self->derive->forget($photo->id);

    if ($arg->{originals}) {
      my $original = $library->original_path($photo);
      if (-e $original) {
        $original->remove or die "can't delete $original: $!\n";
        push @originals, $original;
      }
    }
  }
  $self->derive->save_manifest;

  my $message = @photos == 1
    ? 'remove photo ' . $photos[0]->id
    : sprintf "remove %d photos\n\n%s", 0 + @photos, join qq{\n}, map {; $_->id } @photos;
  $message .= "\n\nTheir originals were deleted too." if @originals;

  return {
    removed   => \@photos,
    albums    => \@albums,
    published => [ map {; $_->id } grep {; $_->is_published } @photos ],
    originals => \@originals,
    commit    => $library->commit_meta($message, @changed),
  };
}

1;
