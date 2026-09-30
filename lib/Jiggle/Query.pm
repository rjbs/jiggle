package Jiggle::Query;
use v5.36;

use Moo;

use Jiggle::Site;

=head1 NAME

Jiggle::Query - pick a batch of photos from a library

=head1 SYNOPSIS

  my $query  = Jiggle::Query->new({ library => $library, terms => [ 'pending' ] });
  my @photos = $query->photos;

=head1 DESCRIPTION

A query is a list of terms, all of which a photo must match:

  pending         not yet reviewed
  private         visibility is private
  public          visibility is public
  all             every photo
  album:SLUG      in the album with that slug
  tag:TAG         tagged TAG, compared as the site compares tags (by slug)
  year:YYYY       taken in that year
  id:ID           the photo with that id; given more than once, any of them

Photos come in album order when the query names an album, and otherwise in
the order they were taken, oldest first, with undated photos last.

An unknown term, or an album that doesn't exist, is an error when the query
is made, not an empty batch.

=cut

has library => (is => 'ro', required => 1);
has terms   => (is => 'ro', required => 1);

has _tests => (is => 'lazy', init_arg => undef);
has _album => (is => 'rw', init_arg => undef);

sub BUILD ($self, $) {
  die "a query needs at least one term\n" unless $self->terms->@*;
  $self->_tests;
}

sub _build__tests ($self) {
  my (@tests, %ids);

  for my $term ($self->terms->@*) {
    if    ($term eq 'all')     { }
    elsif ($term eq 'pending') { push @tests, sub ($p) { $p->pending } }
    elsif ($term eq 'private') { push @tests, sub ($p) { ! $p->is_public } }
    elsif ($term eq 'public')  { push @tests, sub ($p) { $p->is_public } }
    elsif ($term =~ /\Aalbum:(.+)\z/) {
      my $slug = $1;
      my ($album) = grep {; $_->slug eq $slug } $self->library->albums;
      die "no album named $slug\n" unless $album;
      $self->_album($album) unless $self->_album;
      my %in = map {; $_ => 1 } $album->photos->@*;
      push @tests, sub ($p) { $in{ $p->id } };
    }
    elsif ($term =~ /\Atag:(.+)\z/) {
      my $want = Jiggle::Site->tag_slug($1);
      push @tests, sub ($p) {
        grep {; Jiggle::Site->tag_slug($_) eq $want } $p->tags->@*;
      };
    }
    elsif ($term =~ /\Ayear:([0-9]{4})\z/) {
      my $year = $1;
      push @tests, sub ($p) { ($p->taken // '') =~ /\A\Q$year\E-/ };
    }
    elsif ($term =~ /\Aid:(\S+)\z/) {
      $ids{$1} = 1;
    }
    else {
      die "unknown query term: $term\n";
    }
  }

  push @tests, sub ($p) { $ids{ $p->id } } if %ids;

  return \@tests;
}

=method matches

  if ($query->matches($photo)) { ... }

This is true if the photo matches every term.

=cut

sub matches ($self, $photo) {
  for my $test ($self->_tests->@*) {
    return 0 unless $test->($photo);
  }
  return 1;
}

=method photos

This returns the matching photos, in order.

=cut

sub photos ($self) {
  my @found = grep {; $self->matches($_) } $self->library->photos;

  if (my $album = $self->_album) {
    my %pos;
    @pos{ $album->photos->@* } = (0 .. $album->photos->$#*);
    return sort {; $pos{ $a->id } <=> $pos{ $b->id } } @found;
  }

  return sort {;
       (defined $a->taken ? 0 : 1) <=> (defined $b->taken ? 0 : 1)
    || (($a->taken // '') cmp ($b->taken // ''))
    || ($a->id cmp $b->id)
  } @found;
}

1;
