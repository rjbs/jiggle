package Jiggle::Site;
use v5.36;

use Moo;

use Jiggle::Markdown ();
use Encode ();
use HTML::Entities ();
use JSON::MaybeXS ();
use Jiggle::Progress;
use List::Util ();
use XML::LibXML ();
use Time::HiRes ();
use Time::Local ();
use Jiggle::Derive;
use Jiggle::Geo qw( in_private_zone );
use Jiggle::Site::Writer;
use Mojo::ByteStream ();
use Mojo::Template;
use Path::Tiny ();

=head1 NAME

Jiggle::Site - render a library into a static website

=head1 SYNOPSIS

  my $site = Jiggle::Site->new({ library => $library });
  $site->build;

=head1 DESCRIPTION

The whole site is rendered on every build.  That's cheap for HTML, and the
expensive part (renditions) is done ahead of time by L<Jiggle::Derive> and
only hardlinked here.  L<Jiggle::Site::Writer> makes sure that files whose
content didn't change aren't touched, so an incremental sync stays small.

Private photos are treated as though they don't exist.  They're dropped when
the photo list is first assembled, so no page, count, cover, map point, or
feed can mention one.

=cut

has library => (is => 'ro', required => 1);

has out_dir => (
  is   => 'lazy',
  coerce => sub ($d) { Path::Tiny::path($d)->absolute },
  default => sub ($self) { $self->library->root->child('site') },
);

has share_dir => (
  is => 'lazy',
  default => sub { Path::Tiny::path(__FILE__)->absolute->parent(3)->child('share') },
);

has logger => (is => 'ro', default => sub { sub { } });

# A Jiggle::Search, or undef to build without a search index.  (The search
# page is still written; it just won't find anything.)
has search => (is => 'ro');

# The Jiggle::Derive whose manifest says which renditions exist.  The build
# command passes the one it just used.
has derive => (
  is => 'lazy',
  default => sub ($self) { Jiggle::Derive->new({ library => $self->library }) },
);

# If true, check files on disk rather than trusting the manifests; see
# Jiggle::Site::Writer.
has verify => (is => 'ro', default => 0);

has config => (is => 'lazy', default => sub ($self) { $self->library->config });

sub site_title ($self) { $self->config->{title}    // 'Photos' }
# Paths are appended to it, and all start with a slash, so a trailing slash
# here (a natural way to write a URL) is dropped.
sub base_url   ($self) { ($self->config->{base_url} // '') =~ s{/+\z}{}r }

#---------------------------------------------------------------------------
# The model: everything the templates need, with private and pending photos removed.

has photos => (
  is => 'lazy',
  init_arg => undef,
  default  => sub ($self) {
    # A photo whose renditions are missing (because its original couldn't be
    # read) is left out, like a private one, so that nothing links to files
    # that aren't there.
    my (@ready, @missing);
    for my $photo (grep {; $_->is_published } $self->library->photos) {
      if ($self->_renditions_present($photo)) { push @ready, $photo }
      else                                    { push @missing, $photo->id }
    }

    $self->logger->(sprintf 'leaving out %d photo(s) with missing renditions: %s',
      0 + @missing, join q{ }, sort @missing) if @missing;

    # Tags are lowercased when read, so these build as usual, but their
    # files should be fixed.
    my @capital = sort map {; $_->had_capital_tags ? $_->id : () } $self->library->photos;
    $self->logger->(sprintf 'warning: %d photo(s) have tags with capitals in their metadata, used lowercased: %s%s',
      0 + @capital, join(q{ }, List::Util::head(10, @capital)), (@capital > 10 ? q{ ...} : q{})) if @capital;

    # Newest first.  Photos with no date sort last, by id, so the order is at
    # least stable.
    return [
      sort {;
           (defined $b->taken <=> defined $a->taken)
        || (($b->taken // '') cmp ($a->taken // ''))
        || ($a->id cmp $b->id)
      }
      @ready
    ];
  },
);

# Whether a photo's renditions all exist, by derive's manifest; with verify,
# by the files themselves, too.
sub _renditions_present ($self, $photo) {
  return 0 unless $self->derive->is_complete($photo);
  return 1 unless $self->verify;

  for my $recipe (Jiggle::Derive->published_recipes_for($photo)) {
    return 0 unless -e $self->library->derived_path($photo->id, $recipe->{name});
  }
  return 1;
}

has _photo_by_id => (
  is => 'lazy',
  init_arg => undef,
  default  => sub ($self) { return { map {; $_->id => $_ } $self->photos->@* } },
);

# A TOML datetime as epoch seconds, or undef.  Without an offset, it's taken
# as UTC, which is close enough for sorting.
sub _instant ($datetime) {
  my ($y, $mo, $d, $h, $mi, $s, $zone) = ($datetime // '')
    =~ /\A(\d{4})-(\d\d)-(\d\d)T(\d\d):(\d\d):(\d\d)(?:\.\d+)?(Z|[-+]\d\d:\d\d)?\z/
    or return;

  # timegm dies on an impossible date (a typo, say, in hand-edited
  # metadata); that makes the date unusable, not the build.
  my $epoch = eval { Time::Local::timegm($s, $mi, $h, $d, $mo - 1, $y) } // return;
  if ($zone and $zone =~ /\A([-+])(\d\d):(\d\d)\z/) {
    $epoch -= ($1 eq '-' ? -1 : 1) * ($2 * 3600 + $3 * 60);
  }
  return $epoch;
}

=method home_items

  my @items = $site->home_items($n);

This returns the C<$n> newest things for the home page, newest first: each a
published album, which stands for its photos, or a public photo in no
published album.  Each is a photo object or an album hash (as in
L</albums>).

A photo is dated by when it was added, and an album by when its newest photo
was added (or, if none of its photos has a date, when it was created).  So
adding photos to an old album brings it back to the top, and the home page
changes whenever anything new arrives.  (The feed dates albums by creation
instead, so as not to announce an album again.)  Photos added at the same
moment come newest taken first, and anything with no date comes last.

=cut

sub home_items ($self, $n) {
  my %in_album = map {; my $a = $_; map {; $_->id => 1 } $a->{photos}->@* } $self->albums->@*;

  my @items;
  for my $album ($self->albums->@*) {
    my ($when) = sort {; $b <=> $a } grep {; defined } map {; scalar _instant($_->added_at) } $album->{photos}->@*;
    $when //= _instant($album->{created});
    push @items, { when => $when, item => $album, taken => '', name => fc $album->{title} };
  }
  for my $photo (grep {; ! $in_album{ $_->id } } $self->photos->@*) {
    push @items, { when => scalar _instant($photo->added_at), item => $photo,
                   taken => $photo->taken // '', name => $photo->id };
  }

  my @sorted = sort {;
       (defined $b->{when} <=> defined $a->{when})
    || (($b->{when} // 0) <=> ($a->{when} // 0))
    || ($b->{taken} cmp $a->{taken})
    || ($a->{name} cmp $b->{name})
  } @items;

  return map {; $_->{item} } List::Util::head($n, @sorted);
}

=method public_location

  my $loc = $site->public_location($photo);

This returns the photo's location as it may be published, or undef.  A photo
taken inside any private zone in the library's configuration has no public
location, though its metadata keeps the true one.  Neither does a photo whose
location is itself marked private.

Every published location is also rounded, to C<location_precision> decimal
places (3 by default, which is about 100 meters).  That's plenty for a map
of where photos were taken, and it means no published photo pins down an
exact spot.  It also blurs the edge of each private zone, where photos just
outside would otherwise trace a ring around the hidden center.

This is the only place published coordinates come from, so anything that
publishes a location must get it here.

=cut

sub public_location ($self, $photo) {
  my $loc = $photo->location;
  return unless $loc;
  return if $loc->{private};

  # The zone check uses the true location, so rounding can't move a photo
  # out of a zone.
  return if in_private_zone($loc, $self->config->{private_zone} // []);

  my $places = $self->config->{location_precision} // 3;
  return {
    map {; $_ => 0 + sprintf('%.*f', $places, $loc->{$_}) } qw( lat lon )
  };
}

has albums => (
  is => 'lazy',
  init_arg => undef,
  default  => sub ($self) {
    my @albums;

    for my $album ($self->library->albums) {
      my @photos = grep {; defined } map {; $self->_photo_by_id->{$_} }
                   $album->photos->@*;
      next unless @photos;

      # The cover might be unset, or might name a private photo.
      my $cover = $self->_photo_by_id->{ $album->cover // '' } // $photos[0];

      push @albums, {
        slug   => $album->slug,
        title  => $album->title,
        description => $album->description,
        cover  => $cover,
        photos => \@photos,
        created => $album->created,
      };
    }

    # Newest first; albums with no creation date go last, by title.  The
    # dates may carry different offsets, so compare them as instants.
    my %when = map {; $_->{slug} => scalar _instant($_->{created}) } @albums;
    return [
      sort {;
           (defined $when{ $b->{slug} } <=> defined $when{ $a->{slug} })
        || (($when{ $b->{slug} } // 0) <=> ($when{ $a->{slug} } // 0))
        || (fc $a->{title} cmp fc $b->{title})
      } @albums
    ];
  },
);

has _albums_for_photo => (
  is => 'lazy',
  init_arg => undef,
  default  => sub ($self) {
    my %for;
    for my $album ($self->albums->@*) {
      push $for{ $_->id }->@*, $album for $album->{photos}->@*;
    }
    return \%for;
  },
);

sub albums_for ($self, $photo) { $self->_albums_for_photo->{ $photo->id } // [] }

sub tag_slug ($self, $tag) {
  my $slug = lc $tag;
  $slug =~ s/[^\p{Alnum}]+/-/g;
  $slug =~ s/\A-+|-+\z//g;
  return length $slug ? $slug : '-';
}

has tags => (
  is => 'lazy',
  init_arg => undef,
  default  => sub ($self) {
    my %tag;

    for my $photo ($self->photos->@*) {
      for my $name ($photo->tags->@*) {
        my $slug = $self->tag_slug($name);
        $tag{$slug} //= { slug => $slug, name => $name, photos => [] };
        push $tag{$slug}{photos}->@*, $photo;
      }
    }

    return [ sort {; $a->{slug} cmp $b->{slug} } values %tag ];
  },
);

my @MONTHS = qw(
  January February March April May June July
  August September October November December
);

=method archive

This returns the photos grouped by the date they were taken:

  {
    years   => [ { year => 2026, count => 38, months => [ ... ] }, ... ],
    undated => [ @photos ],
  }

Years are newest first, and so are the months within each year.  Each month
is C<< { year, month, photos } >>, with its photos oldest first, so that a
trip reads in order.

Photos are filed by the date on the local clock where they were taken, which
is how a person remembers them: a photo taken at 23:30 on 31 July in Vienna
belongs to July, even though it was already August in UTC.

=cut

has archive => (
  is => 'lazy',
  init_arg => undef,
  default  => sub ($self) {
    my (%by_month, @undated);

    # $self->photos is newest first, so reversing it gives oldest first
    # within each month.
    for my $photo (reverse $self->photos->@*) {
      my ($y, $m) = ($photo->taken // '') =~ /\A(\d{4})-(\d\d)-/;
      if ($y) { push $by_month{$y}{$m}->@*, $photo }
      else    { unshift @undated, $photo }
    }

    my @years = map {;
      my $y = $_;
      my @months = map {;
        { year => $y, month => $_, photos => $by_month{$y}{$_} }
      } sort { $b cmp $a } keys $by_month{$y}->%*;

      {
        year   => $y,
        count  => List::Util::sum(map {; scalar $_->{photos}->@* } @months),
        months => \@months,
      };
    } sort { $b cmp $a } keys %by_month;

    return { years => \@years, undated => \@undated };
  },
);

sub _all_months ($self) {
  map {; $_->{months}->@* } $self->archive->{years}->@*;
}

=method sample

  my @few = $site->sample($n, @photos);

This returns up to C<$n> of the given photos, spread evenly through the list
and kept in order, for showing a preview of a year or month.

=cut

sub sample ($self, $n, @photos) {
  return @photos if @photos <= $n;
  return map {; $photos[ int($_ * @photos / $n) ] } 0 .. $n - 1;
}

#---------------------------------------------------------------------------
# Helpers for templates.

sub year_url  ($self, $year)        { "/$year/" }
sub month_url ($self, $year, $mon)  { "/$year/$mon/" }

sub month_name ($self, $mon) { $MONTHS[$mon - 1] }

# The archive month a photo belongs to, as [ year, month ], or nothing.
sub month_of ($self, $photo) {
  my ($y, $m) = ($photo->taken // '') =~ /\A(\d{4})-(\d\d)-/;
  return $y ? [ $y, $m ] : undef;
}

sub photo_url ($self, $photo) { '/p/' . $photo->id . '/' }

sub rendition_url ($self, $photo, $name) { '/p/' . $photo->id . "/$name" }

sub absolute_url ($self, $path) { $self->base_url . $path }

sub srcset ($self, $photo) {
  join q{, }, map {;
    my ($w) = Jiggle::Derive->rendition_size($photo, $_);
    $self->rendition_url($photo, $_) . " ${w}w";
  } qw( 500.webp 1024.webp 2048.webp );
}

sub rendition_size ($self, $photo, $name) {
  Jiggle::Derive->rendition_size($photo, $name);
}

sub display_date ($self, $photo) {
  my $taken = $photo->taken // return '';
  my ($y, $m, $d, $hm) = $taken =~ /\A(\d{4})-(\d\d)-(\d\d)T(\d\d:\d\d)/;
  return $taken unless $y;
  return sprintf '%d %s %d, %s', $d, $MONTHS[$m - 1], $y, $hm;
}

sub display_title ($self, $photo) {
  return $photo->title if length $photo->title;
  return $self->display_date($photo) || 'Untitled';
}

=method description_html

  my $html = $site->description_html($markdown);

This renders a description (of a photo or an album) with
L<Jiggle::Markdown/markdown_to_html>, as a Mojo::ByteStream so that templates
don't escape it.

=method description_text

This returns a description as plain text, with the Markdown syntax gone, for
places like C<og:description>.

=cut

sub description_html ($self, $text) {
  Mojo::ByteStream->new(Jiggle::Markdown::markdown_to_html($text));
}

sub description_text ($self, $text) {
  Jiggle::Markdown::markdown_to_text($text);
}

=method attr_text

  <link title="<%== $site->attr_text($title) %>">

This escapes text for a double-quoted HTML attribute, escaping only what
has to be: C<&>, C<< < >>, C<< > >>, and C<">.  Templates escape apostrophes
too, which is correct, but some programs read attributes without decoding
entities: Feedbin, finding the feed through its discovery link, named it
"rjbs&#39;s photos".

=cut

sub attr_text ($self, $text) {
  HTML::Entities::encode_entities($text // '', q{<>&"});
}

sub excerpt ($self, $text, $max = 200) {
  $text //= '';
  $text =~ s/\s+/ /g;
  $text =~ s/\A | \z//g;
  return $text if length $text <= $max;
  return substr($text, 0, $max - 1) =~ s/\s+\S*\z//r . "\x{2026}";
}

#---------------------------------------------------------------------------
# Rendering.

has _mt_cache => (is => 'ro', init_arg => undef, default => sub { {} });

sub render ($self, $template, $vars = {}) {
  my $file = $self->share_dir->child('templates', "$template.html.mt");

  my $mt = $self->_mt_cache->{$template} //= Mojo::Template->new(
    vars        => 1,
    auto_escape => 1,
    name        => "$file",
  )->parse($file->slurp_utf8);

  my $out = $mt->process({ site => $self, %$vars });
  die $out if ref $out;  # a Mojo::Exception
  return $out;
}

sub partial ($self, $template, $vars) {
  Mojo::ByteStream->new($self->render($template, $vars));
}

# Mojo::Template declares a template's variables from the keys passed on its
# first use, so every call to the layout must pass the same keys, even when
# their values are undef. -- claude, 2026-09-26
sub render_page ($self, $template, $vars) {
  my $content = $self->render($template, $vars);
  return $self->render('layout', {
    title   => $vars->{title},
    content => Mojo::ByteStream->new($content),
    og      => ($vars->{photo} ? $self->opengraph($vars->{photo}) : undef),
    map     => ($template eq 'map' || $vars->{location}) ? 1 : 0,
  });
}

sub opengraph ($self, $photo) {
  my ($w, $h) = $self->rendition_size($photo, 'og.jpg');
  return {
    title  => $self->display_title($photo),
    url    => $self->absolute_url($self->photo_url($photo)),
    image  => $self->absolute_url($self->rendition_url($photo, 'og.jpg')),
    width  => $w,
    height => $h,
    description => $self->excerpt($self->description_text($photo->description)),
    video  => ($photo->is_video ? $self->_opengraph_video($photo) : undef),
  };
}

sub _opengraph_video ($self, $photo) {
  my ($w, $h) = $self->rendition_size($photo, 'video.mp4');
  return {
    url    => $self->absolute_url($self->rendition_url($photo, 'video.mp4')),
    width  => $w,
    height => $h,
  };
}

has writer => (
  is => 'lazy',
  init_arg => undef,
  default  => sub ($self) {
    Jiggle::Site::Writer->new({
      root     => $self->out_dir,
      manifest => $self->library->state_dir->child('site-manifest.json'),
      verify   => $self->verify,
    });
  },
);

sub _write_page ($self, $rel, $template, $vars) {
  my $html = $self->render_page($template, $vars);
  $self->writer->write_file($rel, Encode::encode('UTF-8', $html));
}

my $JSON = JSON::MaybeXS->new->canonical->utf8;

=method build

This renders the entire site into the output directory, then removes
anything left there from earlier builds that this one didn't produce.

The renditions must already exist; see L<Jiggle::Derive>.

=cut

sub build ($self) {
  my $w = $self->writer;
  my @photos = $self->_phase('reading metadata', sub { $self->photos->@* });

  $self->_write_page('index.html', 'index', {
    title  => $self->site_title,
    items  => [ $self->home_items(100) ],
  });

  $self->_phase('photo pages',     sub { $self->_build_photo_pages(\@photos) });
  $self->_phase('albums and tags', sub { $self->_build_collections });
  $self->_phase('archive',         sub { $self->_build_archive });

  $self->_phase('map, search page, and static files', sub {
    $self->_write_page('map/index.html', 'map', { title => 'Map' });
    $w->write_file('map/photos.geojson', $JSON->encode($self->_geojson));
    $self->_write_page('search/index.html', 'search', { title => 'Search' });
    $self->_write_page('404.html', '404', { title => 'Not found' });
    $w->write_file('feed.xml', $self->feed_xml);
    $self->_copy_static;
  });

  # Pagefind indexes whatever HTML is in the output, so stale pages (like
  # one for a photo just made private) must be pruned before it runs.  The
  # old index is kept until the new one is written, so unchanged index files
  # are left alone.  -- claude, 2026-09-27
  $self->_phase('search index', sub {
    $w->prune({ except => 'pagefind/' });

    # The index is made from the pages, so if none was written or removed,
    # the old index is still right, and Pagefind (which reads every page)
    # needn't run.  keep finds nothing to keep without a manifest.
    if (! $w->html_changes and my $kept = $w->keep('pagefind/')) {
      $self->logger->("search index: no page changed; kept $kept file(s)");
      return;
    }

    if (my $search = $self->search) {
      $search->index_site($self->out_dir, $w);
      return;
    }

    # Without search, a stale index can't be kept: it might still hold the
    # text of a photo made private since.  It's pruned, which leaves the
    # site with no search until it's built again with search.
    # -- claude, 2026-09-29
    $self->logger->('search index: pages changed, and search is off, so the old index '
      . 'is removed; build with search before syncing');
  });

  $self->_phase('pruning', sub { $w->prune; $w->save_manifest });

  my $s = $w->stats;
  $self->logger->(sprintf
    '%d public photo(s); %d file(s) written, %d unchanged, %d linked, %d pruned',
    0 + @photos, @$s{qw( written unchanged linked pruned )},
  );

  return;
}

# Run one phase of the build, and report how long it took.
sub _phase ($self, $name, $code) {
  my $start  = Time::HiRes::time();
  my @result = $code->();
  $self->logger->(sprintf 'site: %s in %.1fs', $name, Time::HiRes::time() - $start);
  return @result;
}

sub _build_photo_pages ($self, $photos) {
  my $w = $self->writer;
  my @photos = @$photos;

  my $progress = Jiggle::Progress->new({
    label  => 'photo pages',
    total  => scalar @photos,
    logger => $self->logger,
  });

  for my $i (keys @photos) {
    $progress->tick;

    my $photo = $photos[$i];
    my $id    = $photo->id;

    $self->_write_page("p/$id/index.html", 'photo', {
      title    => $self->display_title($photo),
      photo    => $photo,
      location => scalar $self->public_location($photo),
      newer    => ($i > 0 ? $photos[$i - 1] : undef),
      older    => $photos[$i + 1],
      albums   => $self->albums_for($photo),
    });

    for my $recipe (Jiggle::Derive->published_recipes_for($photo)) {
      my $source = $self->library->derived_path($id, $recipe->{name});
      $w->link_file("p/$id/$recipe->{name}", $source,
        $self->derive->rendition_key($photo, $recipe->{name}) // '');
    }

    $w->write_file("p/$id/embed.json", $JSON->encode($self->embed_data($photo)));
  }

  return;
}

=method embed_data

  my $data = $site->embed_data($photo);

This returns what another site needs to embed the photo, which is published
as F</p/ID/embed.json>: its title, its page's URL, its type, and the URL and
size of each rendition useful for embedding (and, for a video, of the
video).  URLs are absolute.  It includes no location, and only public photos
are published at all, so a private or unknown id is simply a 404.

C<format> is the version of this structure, for the blog plugin to check.

=cut

sub embed_data ($self, $photo) {
  my $rendition = sub ($name) {
    my ($w, $h) = $self->rendition_size($photo, $name);
    return {
      url    => $self->absolute_url($self->rendition_url($photo, $name)),
      width  => $w,
      height => $h,
    };
  };

  return {
    format => 1,
    id     => $photo->id,
    type   => $photo->type,
    title  => $photo->title,
    alt    => $self->display_title($photo),
    taken  => $photo->taken,
    url    => $self->absolute_url($self->photo_url($photo)),
    width  => $photo->width,
    height => $photo->height,
    renditions => { map {; $_ => $rendition->($_) } qw( 500.webp 1024.webp 2048.webp ) },
    video  => ($photo->is_video ? $rendition->('video.mp4') : undef),
  };
}

sub _build_collections ($self) {
  $self->_write_page('albums/index.html', 'albums', {
    title  => 'Albums',
    albums => $self->albums,
  });

  for my $album ($self->albums->@*) {
    $self->_write_page("albums/$album->{slug}/index.html", 'album', {
      title => $album->{title},
      album => $album,
    });
  }

  $self->_write_page('tags/index.html', 'tags', {
    title => 'Tags',
    tags  => $self->tags,
  });

  for my $tag ($self->tags->@*) {
    $self->_write_page("tags/$tag->{slug}/index.html", 'tag', {
      title => $tag->{name},
      tag   => $tag,
    });
  }

  return;
}

=method feed_entries

This returns the newest 30 entries for the site's feed, newest first.  An
entry is a published album, which stands for all of its photos, or the
public photos in no published album that were taken on one day.  (Photos
with no taken date go by the day they were added.)  A day with just one
such photo is an entry for that photo.

An album is dated by when it was created, and a day or a photo by the most
recent addition to it.  Anything without a date is left out.

Each entry is a hash: C<kind> ("album", "day", or "photo"), C<when> (a
datetime), and C<album>, C<day>, or C<photo>.  A day is
C<< { by => "taken" or "added", date => "2026-01-22", photos => [...] } >>.

=cut

my $FEED_SIZE = 30;

sub feed_entries ($self) {
  my %in_album = map {; my $a = $_; map {; $_->id => 1 } $a->{photos}->@* } $self->albums->@*;

  my @entries;

  for my $album ($self->albums->@*) {
    # An album with no creation date is dated by its newest photo.
    my $when = $album->{created}
            // (List::Util::maxstr(grep {; defined } map {; $_->added_at } $album->{photos}->@*));
    push @entries, { kind => 'album', when => $when, album => $album } if defined $when;
  }

  # Photos in no album are grouped by the day they were taken, or, if they
  # have no taken date, the day they were added, so a week of photos makes
  # a week of entries rather than one per photo.  A day's entry is dated by
  # the newest addition to it, so the feed stays "what's new".
  my %day;
  for my $photo (grep {; ! $in_album{ $_->id } } $self->photos->@*) {
    my $added = $photo->added_at // next;
    my ($date) = ($photo->taken // $added) =~ /\A(\d{4}-\d\d-\d\d)/ or next;
    my $key = defined $photo->taken ? "taken-$date" : "added-$date";
    push $day{$key}->@*, $photo;
  }

  for my $key (sort keys %day) {
    my @photos = sort {; ($a->taken // '') cmp ($b->taken // '') || $a->id cmp $b->id } $day{$key}->@*;
    my ($when) = sort {; (_instant($b) // 0) <=> (_instant($a) // 0) } map {; $_->added_at } @photos;

    if (@photos == 1) {
      push @entries, { kind => 'photo', when => $when, photo => $photos[0] };
    } else {
      my ($by, $date) = $key =~ /\A(taken|added)-(.*)\z/;
      push @entries, { kind => 'day', when => $when, day => { by => $by, date => $date, photos => \@photos } };
    }
  }

  my %epoch = map {; $_->{when} => scalar _instant($_->{when}) } @entries;
  @entries = grep {; defined $epoch{ $_->{when} } } @entries;

  @entries = sort {;
       $epoch{ $b->{when} } <=> $epoch{ $a->{when} }
    || $a->{kind} cmp $b->{kind}
  } @entries;

  return [ List::Util::head($FEED_SIZE, @entries) ];
}

sub _feed_photo_html ($self, $photo) {
  my ($w, $h) = $self->rendition_size($photo, '1024.webp');
  my $url     = $self->absolute_url($self->photo_url($photo));
  my $alt     = HTML::Entities::encode_entities($self->display_title($photo), q{<>&"'});

  my $html = sprintf qq{<p><a href="%s"><img src="%s" width="%d" height="%d" alt="%s"></a></p>\n},
    $url, $self->absolute_url($self->rendition_url($photo, '1024.webp')), $w, $h, $alt;

  $html .= qq{<p><a href="$url">Play the video</a></p>\n} if $photo->is_video;
  $html .= $self->description_html($photo->description);

  if (my @tags = $photo->tags->@*) {
    $html .= "<p>Tags: " . join(', ', map {;
      sprintf '<a href="%s">%s</a>',
        $self->absolute_url('/tags/' . $self->tag_slug($_) . '/'),
        HTML::Entities::encode_entities($_, q{<>&"'})
    } @tags) . "</p>\n";
  }

  return $html;
}

sub _feed_album_html ($self, $album) {
  my $url   = $self->absolute_url("/albums/$album->{slug}/");
  my $cover = $album->{cover};
  my ($w, $h) = $self->rendition_size($cover, '1024.webp');

  my $html = sprintf qq{<p><a href="%s"><img src="%s" width="%d" height="%d" alt=""></a></p>\n},
    $url, $self->absolute_url($self->rendition_url($cover, '1024.webp')), $w, $h;

  $html .= $self->description_html($album->{description});

  my @more = grep {; $_->id ne $cover->id } $self->sample(8, $album->{photos}->@*);
  if (@more) {
    $html .= '<p>' . join(' ', map {;
      my ($tw, $th) = $self->rendition_size($_, 'h480.webp');
      sprintf '<a href="%s"><img src="%s" height="120" width="%d" alt="%s"></a>',
        $self->absolute_url($self->photo_url($_)),
        $self->absolute_url($self->rendition_url($_, 'h480.webp')),
        int(120 * $tw / $th + 0.5),
        HTML::Entities::encode_entities($self->display_title($_), q{<>&"'});
    } @more) . "</p>\n";
  }

  my $n = $album->{photos}->@*;
  $html .= sprintf qq{<p><a href="%s">%d photo%s</a></p>\n}, $url, $n, $n == 1 ? '' : 's';
  return $html;
}

sub _feed_day_html ($self, $day, $url) {
  my @photos = $day->{photos}->@*;
  my @shown  = List::Util::head(12, @photos);

  my $html = '<p>' . join(' ', map {;
    my ($tw, $th) = $self->rendition_size($_, 'h480.webp');
    sprintf '<a href="%s"><img src="%s" height="160" width="%d" alt="%s"></a>',
      $self->absolute_url($self->photo_url($_)),
      $self->absolute_url($self->rendition_url($_, 'h480.webp')),
      int(160 * $tw / $th + 0.5),
      HTML::Entities::encode_entities($self->display_title($_), q{<>&"'});
  } @shown) . "</p>\n";

  my ($y, $m, $d) = split /-/, $day->{date};
  my $date = sprintf '%d %s %d', $d, $self->month_name($m), $y;

  $html .= sprintf qq{<p><a href="%s">%d photos %s %s</a></p>\n},
    $url, scalar @photos, ($day->{by} eq 'taken' ? 'from' : 'added'), $date;

  return $html;
}

=method feed_xml

This returns the site's Atom feed, as bytes.  See L</feed_entries>.

=cut

sub feed_xml ($self) {
  my $ATOM = 'http://www.w3.org/2005/Atom';
  my $doc  = XML::LibXML::Document->new('1.0', 'UTF-8');
  my $feed = $doc->createElementNS($ATOM, 'feed');
  $doc->setDocumentElement($feed);

  my $add = sub ($parent, $name, $text = undef, %attr) {
    my $el = $doc->createElementNS($ATOM, $name);
    $el->setAttribute($_ => $attr{$_}) for sort keys %attr;
    $el->appendText($text) if defined $text;
    $parent->appendChild($el);
    return $el;
  };

  my $entries = $self->feed_entries;
  my $home    = $self->absolute_url('/');

  $add->($feed, 'title', $self->site_title);
  $add->($feed, 'id', $home);
  $add->($feed, 'link', undef, rel => 'alternate', type => 'text/html', href => $home);
  $add->($feed, 'link', undef, rel => 'self', type => 'application/atom+xml',
    href => $self->absolute_url('/feed.xml'));
  $add->($feed, 'updated', @$entries ? $entries->[0]{when} : '1970-01-01T00:00:00Z');
  $add->($add->($feed, 'author'), 'name', $self->config->{author} // $self->site_title);

  for my $entry (@$entries) {
    my $el = $add->($feed, 'entry');

    my ($title, $url, $html, @tags);
    my $id;
    if ($entry->{kind} eq 'album') {
      my $album = $entry->{album};
      ($title, $url, $html) = ($album->{title}, $self->absolute_url("/albums/$album->{slug}/"),
                               $self->_feed_album_html($album));
    } elsif ($entry->{kind} eq 'day') {
      my $day    = $entry->{day};
      my @photos = $day->{photos}->@*;
      my ($y, $m) = $day->{date} =~ /\A(\d{4})-(\d\d)/;

      $title = sprintf '%s, and %d more', $self->display_title($photos[0]), @photos - 1;
      $url   = $self->absolute_url($day->{by} eq 'taken' ? $self->month_url($y, $m) : '/archive/undated/');
      $html  = $self->_feed_day_html($day, $url);
      @tags  = do { my %seen; grep {; ! $seen{$_}++ } map {; $_->tags->@* } @photos };

      # Many days share a month page, so the entry's id can't be its URL.  A
      # tag URI names the day itself, so it stays the same when the day gets
      # more photos later.
      my ($host) = $self->base_url =~ m{\A\w+://([^/:]+)};
      $id = sprintf 'tag:%s,2026:day/%s/%s', $host // 'localhost', $day->{by}, $day->{date};
    } else {
      my $photo = $entry->{photo};
      ($title, $url, $html) = ($self->display_title($photo), $self->absolute_url($self->photo_url($photo)),
                               $self->_feed_photo_html($photo));
      @tags = $photo->tags->@*;
    }

    $add->($el, 'title', $title);
    $add->($el, 'id', $id // $url);
    $add->($el, 'link', undef, rel => 'alternate', type => 'text/html', href => $url);
    $add->($el, 'published', $entry->{when});
    $add->($el, 'updated', $entry->{when});
    $add->($el, 'category', undef, term => $_) for @tags;
    $add->($el, 'content', "$html", type => 'html');
  }

  return $doc->toString(1);
}

sub _build_archive ($self) {
  my $archive = $self->archive;
  my @years   = $archive->{years}->@*;

  $self->_write_page('archive/index.html', 'archive', {
    title   => 'Archive',
    years   => \@years,
    undated => $archive->{undated},
  });

  if ($archive->{undated}->@*) {
    $self->_write_page('archive/undated/index.html', 'undated', {
      title  => 'Undated',
      photos => $archive->{undated},
    });
  }

  # Years and months are newest first, so the "newer" neighbor of each is the
  # one before it in the list.
  for my $i (keys @years) {
    my $year = $years[$i];
    $self->_write_page("$year->{year}/index.html", 'year', {
      title => $year->{year},
      year  => $year,
      newer => ($i > 0 ? $years[$i - 1] : undef),
      older => $years[$i + 1],
    });
  }

  my @months = $self->_all_months;
  for my $i (keys @months) {
    my $month = $months[$i];
    $self->_write_page("$month->{year}/$month->{month}/index.html", 'month', {
      title => $self->month_name($month->{month}) . " $month->{year}",
      month => $month,
      newer => ($i > 0 ? $months[$i - 1] : undef),
      older => $months[$i + 1],
    });
  }

  return;
}

sub _geojson ($self) {
  my @features;

  for my $photo ($self->photos->@*) {
    my $loc = $self->public_location($photo) or next;
    push @features, {
      type     => 'Feature',
      geometry => { type => 'Point', coordinates => [ @$loc{qw( lon lat )} ] },
      properties => {
        title => $self->display_title($photo),
        url   => $self->photo_url($photo),
        thumb => $self->rendition_url($photo, 'h480.webp'),
      },
    };
  }

  return { type => 'FeatureCollection', features => \@features };
}

sub _copy_static ($self) {
  my $static = $self->share_dir->child('static');
  my $iter = $static->iterator({ recurse => 1 });

  while (my $file = $iter->()) {
    next if $file->is_dir or $file->basename =~ /\A\./;
    my $rel = $file->relative($static);
    $self->writer->write_file("static/$rel", $file->slurp_raw);
  }
}

1;
