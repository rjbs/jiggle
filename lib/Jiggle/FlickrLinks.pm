package Jiggle::FlickrLinks;
use v5.36;

use Moo;

=head1 NAME

Jiggle::FlickrLinks - rewrite links to Flickr as links to the jiggle site

=head1 SYNOPSIS

  my $rewriter = Jiggle::FlickrLinks->new({
    base_url => 'https://photos.example.com',
    photos   => { $flickr_id => $jiggle_id, ... },   # published photos only
    albums   => { $flickr_set_id => $slug, ... },    # published albums only
  });

  my ($new_text, $changes, $problems) = $rewriter->rewrite($text);

=head1 DESCRIPTION

This rewrites a document (a blog post, say) that links to the owner's
Flickr photos:

=for :list
* an embedded photo, meaning a link to a Flickr photo page (or C<flic.kr>
short link) around an image from Flickr's static servers, becomes
C<{% photo ID %}>, the blog's tag for embedding a jiggle photo
* an image from Flickr with no link around it becomes C<{% photo ID %}>, too
* a link to a photo page, or a C<flic.kr> short link, becomes a link to the
photo's page on the jiggle site
* a link to an album (C<albums/N> or C<sets/N>) becomes a link to the album's
page
* a link to the owner's photostream becomes a link to the site's home page

Only photos and albums that are published can be linked to, so the maps
should hold only those.  Anything that can't be mapped is left as it was and
reported: someone else's Flickr link, a private or missing photo, or an
embed whose link and image name different photos.

C<rewrite> returns the new text, a list of changes (each C<[ $old, $new ]>),
and a list of problems (each C<[ $text, $reason ]>).

=cut

has base_url => (is => 'ro', required => 1, coerce => sub ($u) { $u =~ s{/+\z}{}r });
has photos   => (is => 'ro', required => 1);
has albums   => (is => 'ro', required => 1);

# Whose Flickr links are to be rewritten: the username, or the NSID.
has users => (is => 'ro', default => sub { [ 'rjbs', '51035772155@N01' ] });

has _user_re => (
  is => 'lazy',
  default => sub ($self) {
    my $users = join q{|}, map {; quotemeta } $self->users->@*;
    qr{(?:$users)}i;
  },
);

# A URL's end: anything but whitespace, quotes, angle brackets, or a closing
# paren or bracket, which in Markdown or HTML end the URL.
my $TAIL = qr{[^\s"'<>)\]]*};

sub _photo_page_re ($self) {
  my $user = $self->_user_re;
  qr{https?://(?:www\.)?flickr\.com/photos/$user/(\d+)(?:/$TAIL)?}i;
}

my $SHORT_RE  = qr{https?://flic\.kr/p/([1-9a-km-zA-HJ-NP-Z]+)};
my $STATIC_RE = qr{https?://[a-z0-9.]*?static\.?flickr\.com/(?:\d+/)+(\d+)_[0-9a-f]+(?:_[a-z0-9]+)?\.(?:jpe?g|png|gif)}i;

=func decode_short_id

  my $flickr_id = decode_short_id('2psFuj3');

C<flic.kr/p/> links name a photo by its id in base 58, with Flickr's own
alphabet, which leaves out 0, O, I, and l.

=cut

my $BASE58 = '123456789abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ';
my %DIGIT  = map {; substr($BASE58, $_, 1) => $_ } 0 .. 57;

sub decode_short_id ($short) {
  my $n = 0;
  $n = $n * 58 + $DIGIT{$_} for split //, $short;
  return $n;
}

sub _flickr_id_of_url ($self, $url) {
  my $page = $self->_photo_page_re;
  return $1 if $url =~ /\A$page\z/;
  return decode_short_id($1) if $url =~ /\A$SHORT_RE\z/;
  return $1 if $url =~ /\A$STATIC_RE\z/;
  return;
}

sub _embed ($self, $flickr_id, $problems, $original) {
  my $id = $self->photos->{$flickr_id};
  unless ($id) {
    push @$problems, [ $original, "Flickr photo $flickr_id isn't published on the new site" ];
    return $original;
  }
  return "{% photo $id %}";
}

sub _photo_url ($self, $flickr_id, $problems, $original) {
  my $id = $self->photos->{$flickr_id};
  unless ($id) {
    push @$problems, [ $original, "Flickr photo $flickr_id isn't published on the new site" ];
    return $original;
  }
  return $self->base_url . "/p/$id/";
}

sub rewrite ($self, $text) {
  my (@changes, @problems, @kept);

  my $user = $self->_user_re;
  my $page = $self->_photo_page_re;
  my $link = qr{$page|$SHORT_RE};

  my $record = sub ($old, $new) {
    push @changes, [ $old, $new ] unless $old eq $new;
    return $new;
  };

  # 1. An embed: a link to a photo, around an image from Flickr.  The parts
  # are named, because the patterns spliced in have groups of their own,
  # which would throw off numbered ones.
  $text =~ s{
    (?<whole>
      <a\b[^>]*?\bhref=(?<q1>["'])(?<href>$link)\k<q1>[^>]*>
      \s*
      <img\b[^>]*?\bsrc=(?<q2>["'])(?<src>$STATIC_RE)\k<q2>[^>]*?/?>
      \s*
      </a>
    )
  }{
    my ($whole, $href, $src) = @+{qw( whole href src )};
    my ($link_id, $image_id) = ($self->_flickr_id_of_url($href), $self->_flickr_id_of_url($src));

    my $new;
    if ($link_id and $image_id and $link_id != $image_id) {
      push @problems, [ $whole, "the link is to $link_id but the image is of $image_id" ];
      $new = $whole;
    } else {
      $new = $record->($whole, $self->_embed($link_id // $image_id, \@problems, $whole));
    }

    # An embed left as it was (because it's a problem) is set aside until the
    # end, so the later steps don't rewrite its link and image separately:
    # something flagged for a person to look at should stay as written.
    if ($new eq $whole) {
      push @kept, $whole;
      "\0KEPT" . $#kept . "\0";
    } else {
      $new;
    }
  }gsiex;

  # 1b. An embedded video: a link to a photo, around a <video> that plays it
  # from Flickr (its src is the photo page's /play/ URL).  The closing </a>
  # is optional, because one old post leaves it off; replacing the fragment
  # fixes that, too.
  $text =~ s{
    (?<whole>
      <a\b[^>]*?\bhref=(?<q1>["'])(?<href>$link)\k<q1>[^>]*>
      \s*
      <video\b[^>]*>.*?</video>
      (?:\s*</a>)?
    )
  }{
    my ($whole, $href) = @+{qw( whole href )};
    my $new = $record->($whole, $self->_embed($self->_flickr_id_of_url($href), \@problems, $whole));

    if ($new eq $whole) {
      push @kept, $whole;
      "\0KEPT" . $#kept . "\0";
    } else {
      $new;
    }
  }gsiex;

  # 2. An image from Flickr with no link: HTML, or Markdown.
  $text =~ s{(<img\b[^>]*?\bsrc=(["'])($STATIC_RE)\2[^>]*?/?>)}{
    $record->($1, $self->_embed($self->_flickr_id_of_url($3), \@problems, $1));
  }gsie;

  $text =~ s{(!\[[^\]]*\]\(($STATIC_RE)\))}{
    $record->($1, $self->_embed($self->_flickr_id_of_url($2), \@problems, $1));
  }gsie;

  # 3. Links to photos, wherever they are.
  $text =~ s{($link)}{
    $record->($1, $self->_photo_url($self->_flickr_id_of_url($1), \@problems, $1));
  }gie;

  # 4. Links to albums.
  $text =~ s{(https?://(?:www\.)?flickr\.com/photos/$user/(?:albums|sets)/(\d+)/?)}{
    my ($url, $set) = ($1, $2);
    my $slug = $self->albums->{$set};
    unless ($slug) {
      push @problems, [ $url, "Flickr album $set isn't published on the new site" ];
    }
    $record->($url, $slug ? $self->base_url . "/albums/$slug/" : $url);
  }gie;

  # 5. The photostream itself.
  $text =~ s{(https?://(?:www\.)?flickr\.com/photos/$user/?)(?=["'\s)<\]]|\z)}{
    $record->($1, $self->base_url . '/');
  }gie;

  $text =~ s{\0KEPT(\d+)\0}{$kept[$1]}g;

  # Anything left that points at Flickr is reported, not changed, unless it
  # was part of something already reported.
  while ($text =~ m{(https?://(?:[a-z0-9]+\.)*(?:flickr\.com|flic\.kr|staticflickr\.com)/$TAIL)}gi) {
    my $url = $1;
    next if grep {; index($_->[0], $url) >= 0 } @problems;
    # An image URL doesn't name its owner, but its photo id does, if the
    # photo is one of ours.
    my ($image_id) = $url =~ /\A$STATIC_RE\z/;

    my $reason = $url =~ m{flickr\.com/(?:photos/)?$user\b}i
                   ? "a Flickr link of a kind this doesn't rewrite"
               : $image_id && $self->photos->{$image_id}
                   ? "an image URL outside an embed (the embed around it may be malformed)"
               : "not one of the owner's photos; left alone";

    push @problems, [ $url, $reason ];
  }

  return ($text, \@changes, \@problems);
}

1;
